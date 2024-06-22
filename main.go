package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/dop251/goja"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
	"github.com/vanng822/css"
)

type Component struct {
	Name string
	Path string
}

// Render renders the template with the given data
func Render(path string, props map[string]any) (string, string, string) {

	c, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	template := string(c)

	// Split template into parts
	markup, fence, script, style := templateParts(template)
	// Get list of imported components and remove imports from fence
	fence, components := getComponents(fence)
	// Set the prop to the value that's passed in
	fence = setProps(fence, props)
	// Run the JS in Goja to get the computed values for props
	props = evaluateProps(fence, props)
	// Replace any simple vars in the format {myProp} with the value
	markup = applyProps(markup, props)
	// Run template conditions {if}{else}{/if}
	markup = renderConditions(markup, props)
	// Run template loops {for let _ in _}{/for} and {for let _ of _}{/for}
	markup = renderLoops(markup, props)
	// Recursively render imported components
	markup, script, style = renderComponents(markup, script, style, props, components)
	// Add scoped classes to html
	markup, script, style = scopedClasses(markup, script, style)

	ast, err := js.Parse(parse.NewInputString(script), js.Options{})
	if err != nil {
		panic(err)
	}
	script = ast.JSString()

	return markup, script, style
}

type scopedElement struct {
	tag         string
	id          string
	classes     []string
	scopedClass string
}

func scopedClasses(markup, script, style string) (string, string, string) {
	scopedElements := []scopedElement{}
	node, _ := html.Parse(strings.NewReader(markup))
	var traverse func(*html.Node)
	traverse = func(node *html.Node) {
		if node.Type == html.ElementNode {
			tag := node.Data
			id := ""
			classes := []string{}
			scopedClass := getTagScopedClass(tag, scopedElements)

			if scopedClass == "" {
				// There wasn't an existing scoped class for the element, so create one
				randomStr, err := generateRandom()
				if err != nil {
					log.Fatal(err)
				}
				scopedClass = "plenti-" + randomStr
			}

			for i, attr := range node.Attr {
				if attr.Key == "id" {
					id = attr.Val
				}
				if attr.Key == "class" {
					classes = strings.Split(attr.Val, " ")
					alreadyScoped := false
					for _, class := range classes {
						if strings.HasPrefix(class, "plenti-") {
							alreadyScoped = true
						}
					}
					if !alreadyScoped {
						node.Attr[i].Val += " " + scopedClass
					}
				}
			}

			if len(classes) == 0 {
				node.Attr = append(node.Attr, html.Attribute{Key: "class", Val: scopedClass})
			}

			scopedElements = append(scopedElements, scopedElement{
				tag:         tag,
				id:          id,
				classes:     classes,
				scopedClass: scopedClass,
			})
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			traverse(child)
		}
	}
	traverse(node)

	// Render the modified HTML back to a string
	buf := &strings.Builder{}
	err := html.Render(buf, node)
	if err != nil {
		log.Fatal(err)
	}
	markup = html.UnescapeString(buf.String())

	return markup, script, style
}

type selector struct {
	element string
	classes []string
	id      string
}

func scopedCSS(style string) {
	ss := css.Parse(style)
	rules := ss.GetCSSRuleList()
	for _, rule := range rules {
		tokens := rule.Style.Selector.Tokens
		selectors := []selector{{}}
		fmt.Println("\nNEW SELECTOR")
		selector_index := 0
		for i, token := range tokens {
			if token.Type.String() == "S" && i+1 != len(tokens) {
				// Space indicates a nested selector
				selector_index++
				selectors = append(selectors, selector{})
			}
			if token.Type.String() == "IDENT" && (i < 1 || tokens[i-1].Value != ".") {
				element := token.Value
				selectors[selector_index].element = element
			}
			if token.Type.String() == "CHAR" && token.Value == "." && i+1 > len(tokens) {
				class := tokens[i+1].Value
				selectors[selector_index].classes = append(selectors[selector_index].classes, class)
			}
			if token.Type.String() == "HASH" {
				id := strings.TrimPrefix(token.Value, "#")
				selectors[selector_index].id = id
			}
		}
		fmt.Println(selectors)
	}

}

func getTagScopedClass(tag string, scopedElements []scopedElement) string {
	for _, elem := range scopedElements {
		if elem.tag == tag {
			return elem.scopedClass
		}
	}
	return ""
}

func generateRandom() (string, error) {
	chars := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var bytes = make([]byte, 6)
	for i := range bytes {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		bytes[i] = chars[num.Int64()]
	}
	return string(bytes), nil
}

func templateParts(template string) (string, string, string, string) {
	reFence := regexp.MustCompile(`(?s)---(.*?)---`)
	reScript := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	reStyle := regexp.MustCompile(`(?s)<style>(.*?)</style>`)
	fences := reFence.FindAllStringSubmatch(template, -1)
	scripts := reScript.FindAllStringSubmatch(template, -1)
	styles := reStyle.FindAllStringSubmatch(template, -1)
	if len(fences) > 1 {
		log.Fatal("Can only have one set of Fences (--- and ---) per template")
	}
	if len(scripts) > 1 {
		log.Fatal("Can only have one set of Script tags (<script></script>) per template")
	}
	if len(styles) > 1 {
		log.Fatal("Can only have one set of Style tags (<style></style>) per template")
	}
	markup := template
	fence := ""
	script := ""
	style := ""
	if len(fences) > 0 {
		wrapped_fence := fences[0][0]
		fence = fences[0][1]
		markup = strings.Replace(markup, wrapped_fence, "", 1)
	}
	if len(scripts) > 0 {
		wrapped_script := scripts[0][0]
		script = scripts[0][1]
		markup = strings.Replace(markup, wrapped_script, "", 1)
	}
	if len(styles) > 0 {
		wrapped_style := styles[0][0]
		style = styles[0][1]
		markup = strings.Replace(markup, wrapped_style, "", 1)
	}
	return markup, fence, script, style
}

func setProps(script string, props map[string]any) string {
	for name, value := range props {
		reProp := regexp.MustCompile(fmt.Sprintf(`let (%s);`, name))
		switch value := value.(type) {
		case string:
			script = reProp.ReplaceAllString(script, "let "+name+"='"+value+"';")
		case int:
			script = reProp.ReplaceAllString(script, "let "+name+"="+strconv.Itoa(value)+";")
		case int64:
			script = reProp.ReplaceAllString(script, "let "+name+"="+strconv.Itoa(int(value))+";")
		default:
			// handle other values
			fmt.Println(reflect.TypeOf(value))
		}
	}
	return script
}

func evaluateProps(script string, props map[string]any) map[string]any {
	vm := goja.New()
	vm.RunString(script)
	for name := range props {
		evaluated_value := vm.Get(name).Export()
		props[name] = evaluated_value
	}
	return props
}

func applyProps(markup string, props map[string]any) string {
	// Replace placeholders with data
	for name, value := range props {
		reTextNodesOnly := regexp.MustCompile(fmt.Sprintf(`(>.*?)({%s})(.*?<)`, name)) // TODO: Only temp replacing textnodes to avoid conflicts with props
		switch value := value.(type) {
		case string:
			markup = reTextNodesOnly.ReplaceAllString(markup, `${1}`+value+`${3}`)
		case int:
			markup = reTextNodesOnly.ReplaceAllString(markup, `${1}`+strconv.Itoa(value)+`${3}`)
		case int64:
			markup = reTextNodesOnly.ReplaceAllString(markup, `${1}`+strconv.Itoa(int(value))+`${3}`)
		default:
			// handle other values
			fmt.Println(reflect.TypeOf(value))
		}
	}
	return markup
}

func renderConditions(markup string, props map[string]any) string {
	reCondition := regexp.MustCompile(`(?s){(if)\s(.*?)}(.*?)(?:{(?:(else\sif)\s(.*?)}(.*?)|(?:(else))}(.*?))){0,}{/if}`)
	matches := reCondition.FindAllStringSubmatch(markup, -1)
	for _, match := range matches {
		full_match := match[0]
		for i, part := range match {
			if part == "if" || part == "else if" {
				condition := match[i+1]
				result := match[i+2]
				if evaluateCondition(condition, props) {
					markup = strings.Replace(markup, full_match, result, 1)
					break
				}
			}
			if part == "else" {
				result := match[i+1]
				markup = strings.Replace(markup, full_match, result, 1)
				break
			}
		}
		markup = strings.Replace(markup, full_match, "", 1) // Did not match any conditions, just remove it
	}
	return markup
}

func evaluateCondition(condition string, props map[string]any) bool {
	vm := goja.New()
	l := js.NewLexer(parse.NewInputString(condition))
	for {
		tt, text := l.Next()
		switch tt {
		case js.ErrorToken:
			if l.Err() != io.EOF {
				fmt.Println("Error: ", l.Err())
			}
			result, err := vm.RunString(condition)
			if err != nil {
				fmt.Println("For condition: " + condition)
				log.Fatal(err)
			}
			return result.ToBoolean()
		case js.IdentifierToken:
			value, ok := props[string(text)]
			if ok {
				condition = strings.Replace(condition, string(text), anyToString(value), 1)
			}
		default:
			//fmt.Println("Token Type is: " + js.TokenType(tt).String())
		}
	}
}

func renderLoops(markup string, props map[string]any) string {
	reLoop := regexp.MustCompile(`(?s){for\slet\s(.*?)\s(of|in)\s(.*?)}(.*?){/for}`)
	matches := reLoop.FindAllStringSubmatch(markup, -1)
	for _, match := range matches {
		full_match := match[0]
		for i, part := range match {
			if part == "of" {
				iterator := match[i-1]
				collection := match[i+1]
				result := match[i+2]
				full_result := ""
				collection_value, ok := props[collection]
				if !ok {
					collection_value = collection
				}
				items := evaluateLoop(anyToString(collection_value))
				for _, value := range items {
					reLoopVar := regexp.MustCompile(`{` + iterator + `}`)
					evaluated_result := reLoopVar.ReplaceAllString(result, value)
					full_result = full_result + evaluated_result
				}
				markup = strings.Replace(markup, full_match, full_result, 1)
				break
			}
			if part == "in" {
				result := match[i+1]
				markup = strings.Replace(markup, full_match, result, 1)
				break
			}
		}
		markup = strings.Replace(markup, full_match, "", 1) // Did not match any conditions, just remove it
	}
	return markup
}

func evaluateLoop(collection_value string) []string {
	vm := goja.New()
	v, err := vm.RunString(collection_value)
	if err != nil {
		log.Fatal(err)
	}
	var r []string
	err = vm.ExportTo(v, &r)
	if err != nil {
		log.Fatal(err)
	}
	return r
}

func getComponents(fence string) (string, []Component) {
	components := []Component{}
	reImport := regexp.MustCompile(`import\s+([A-Za-z_][A-Za-z_0-9]*)\s+from\s*"([^"]+)";`)
	for _, line := range strings.Split(fence, "\n") {
		match := reImport.FindStringSubmatch(line)
		if len(match) > 1 {
			compName := match[1]
			compPath := match[2]
			components = append(components, Component{
				Name: compName,
				Path: compPath,
			})
			fence = reImport.ReplaceAllString(fence, "") // Remove current import so script can run in goja
		}
	}
	return fence, components
}

func renderComponents(markup, script, style string, props map[string]any, components []Component) (string, string, string) {
	// Recursively render imports
	for _, component := range components {
		reComponent := regexp.MustCompile(fmt.Sprintf(`<%s(.*?)/>`, component.Name))
		matches := reComponent.FindAllStringSubmatch(markup, -1)
		for _, match := range matches {
			if len(match) > 1 {
				comp_props := map[string]any{}
				reProp := regexp.MustCompile(`{(.*?)}`)
				wrapped_props := reProp.FindAllStringSubmatch(match[1], -1)
				for _, wrapped_prop := range wrapped_props {
					prop_name := wrapped_prop[1]
					prop_value := props[prop_name]
					comp_props[prop_name] = prop_value
				}
				comp_markup, comp_script, comp_style := Render(component.Path, comp_props)
				markup = reComponent.ReplaceAllString(markup, comp_markup)
				script = script + comp_script
				style = style + comp_style
			}
		}
	}
	return markup, script, style
}

func anyToString(value any) string {
	switch value := value.(type) {
	case string:
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			return value
		}
		return "'" + value + "'"
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.Itoa(int(value))
	default:
		fmt.Println(reflect.TypeOf(value))
		return ""
	}
}

func main() {
	// Render the template with data
	props := map[string]any{"name": "John", "age": 22}
	markup, script, style := Render("views/home.html", props)
	scopedCSS(style)
	os.WriteFile("./public/script.js", []byte(script), fs.ModePerm)
	os.WriteFile("./public/style.css", []byte(style), fs.ModePerm)
	os.WriteFile("./public/index.html", []byte(markup), fs.ModePerm)

	http.Handle("/", http.FileServer(http.Dir("./public")))
	fmt.Println("visit site at: http://localhost:3000")
	http.ListenAndServe(":3000", nil)
}
