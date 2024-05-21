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
func Render(name, path string, props map[string]any) string {

	c, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	content := string(c)

	// Get list of imported components
	content, components, script := processFence(content)

	script = setProps(script, props)
	props = evaluateProps(script, props)
	content = applyProps(content, props)
	content = renderConditions(content, props)

	// Recursively render imports
	for _, component := range components {
		reComponent := regexp.MustCompile(fmt.Sprintf(`<%s(.*?)/>`, component.Name))
		matches := reComponent.FindAllStringSubmatch(content, -1)
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
				renderedComp := Render(component.Name, component.Path, comp_props)
				content = reComponent.ReplaceAllString(content, renderedComp)
			}
		}
	}

	return content
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
	reCondition := regexp.MustCompile(`(?s){if\s(.*?)}(.*?)({else}?)(.*?)({/if})`)
	matches := reCondition.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) > 0 {
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
		}
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
			result, _ := vm.RunString(condition)
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

func processFence(content string) (string, []Component, string) {
	components := []Component{}
	reFence := regexp.MustCompile(`(?s)---(.*?)---`)
	fence := reFence.FindStringSubmatch(content)
	script := ""
	if len(fence) > 1 {
		fenceContents := fence[1]
		script = fenceContents
		for _, line := range strings.Split(fenceContents, "\n") {
			reImport := regexp.MustCompile(`import\s+([A-Za-z_][A-Za-z_0-9]*)\s+from\s*"([^"]+)";`)
			match := reImport.FindStringSubmatch(line)
			if len(match) > 1 {
				compName := match[1]
				compPath := match[2]
				components = append(components, Component{
					Name: compName,
					Path: compPath,
				})
				script = reImport.ReplaceAllString(script, "") // Remove current import so script can run in goja
			}
		}
		content = reFence.ReplaceAllString(content, "") // Remove fence entirely
	}
	return content, components, script
}

func anyToString(value any) string {
	switch value := value.(type) {
	case string:
		return value
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
	props := map[string]any{"name": "John", "age": 25}
	rendered := Render("Home", "views/home.html", props)
	fmt.Println(rendered)
}
