package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
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
	// Recursively render imported components
	markup, script, style = renderComponents(markup, script, style, props, components)

	return markup, script, style
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
	dom := template
	fence := ""
	script := ""
	style := ""
	if len(fences) > 0 {
		wrapped_fence := fences[0][0]
		fence = fences[0][1]
		dom = strings.Replace(template, wrapped_fence, "", 1)
	}
	if len(scripts) > 0 {
		wrapped_script := scripts[0][0]
		script = scripts[0][1]
		dom = strings.Replace(template, wrapped_script, "", 1)
	}
	if len(styles) > 0 {
		wrapped_style := styles[0][0]
		style = styles[0][1]
		dom = strings.Replace(template, wrapped_style, "", 1)
	}
	return dom, fence, script, style
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

func applyProps(content string, props map[string]any) string {
	// Replace placeholders with data
	for name, value := range props {
		reTextNodesOnly := regexp.MustCompile(fmt.Sprintf(`(>.*?)({%s})(.*?<)`, name)) // TODO: Only temp replacing textnodes to avoid conflicts with props
		switch value := value.(type) {
		case string:
			content = reTextNodesOnly.ReplaceAllString(content, `${1}`+value+`${3}`)
		case int:
			content = reTextNodesOnly.ReplaceAllString(content, `${1}`+strconv.Itoa(value)+`${3}`)
		case int64:
			content = reTextNodesOnly.ReplaceAllString(content, `${1}`+strconv.Itoa(int(value))+`${3}`)
		default:
			// handle other values
			fmt.Println(reflect.TypeOf(value))
		}
	}
	return content
}

func renderConditions(content string, props map[string]any) string {
	//reCondition := regexp.MustCompile(`(?s){if\s(.*?)}(.*?)({else}?)(.*?)({/if})`)
	//reCondition := regexp.MustCompile(`(?s){if\s(.*?)}(.*?)({else\sif\s?)({else?)}\s(.*?)({/if})`)
	reCondition := regexp.MustCompile(`(?s){(if)\s(.*?)}(.*?)(?:{(?:(else\sif)\s(.*?)}(.*?)|(?:(else))}(.*?))){0,}{/if}`)
	matches := reCondition.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		for i, part := range match {
			if part == "if" || part == "else if" {
				//fmt.Println(match[i+1])
				//fmt.Println(match[i+2])
				condition := match[i+1]
				trueContent := match[i+2]
				if evaluateCondition(condition, props) {
					content = reCondition.ReplaceAllString(content, trueContent)
					break
				}
			}
			if part == "else" {
				trueContent := match[i+1]
				content = reCondition.ReplaceAllString(content, trueContent)
				break
			}
			//fmt.Println(strconv.Itoa(i) + ": /////////////////")
			//fmt.Println(part)
		}
		/*
			condition := match[1]
			trueContent := match[2]
			falseContent := ""
			if match[3] == "{else}" {
				falseContent = match[4]
			}
			if evaluateCondition(condition, props) {
				content = reCondition.ReplaceAllString(content, trueContent)
			} else {
				content = reCondition.ReplaceAllString(content, falseContent)
			}
		*/
	}
	return content
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
			fmt.Println(condition)
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
	props := map[string]any{"name": "John", "age": 5}
	markup, script, style := Render("views/home.html", props)
	fmt.Println(markup)
	fmt.Println(script)
	fmt.Println(style)
}
