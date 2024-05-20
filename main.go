package main

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

type Component struct {
	Name    string
	Path    string
	Content string
}

// Render renders the template with the given data
func Render(content string, data map[string]any) string {

	// Get list of imported components
	content, components, script := processFence(content)

	// Replace placeholders with data
	for name, value := range data {
		reProp := regexp.MustCompile(fmt.Sprintf(`let (%s);`, name))
		reTextNodesOnly := regexp.MustCompile(fmt.Sprintf(`(>.*?)({%s})(.*?<)`, name)) // TODO: Only temp replacing textnodes to avoid conflicts with props
		switch value := value.(type) {
		case string:
			script = reProp.ReplaceAllString(script, "let "+name+"='"+value+"';")
			content = reTextNodesOnly.ReplaceAllString(content, `${1}`+value+`${3}`)
		case int:
			script = reProp.ReplaceAllString(script, "let "+name+"="+strconv.Itoa(value)+";")
			content = reTextNodesOnly.ReplaceAllString(content, `${1}`+strconv.Itoa(value)+`${3}`)
		default:
			// handle other values
			fmt.Println(reflect.TypeOf(value))
		}
	}
	fmt.Println(script)

	vm := goja.New()
	vm.RunString(script)
	// Recursively render imports
	for _, component := range components {
		reComponent := regexp.MustCompile(fmt.Sprintf(`<%s(.*?)/>`, component.Name))
		matches := reComponent.FindAllStringSubmatch(content, -1)
		for _, match := range matches {
			if len(match) > 1 {
				passed_data := map[string]any{}
				reProp := regexp.MustCompile(`{(.*?)}`)
				wrapped_props := reProp.FindAllStringSubmatch(match[1], -1)
				for _, wrapped_prop := range wrapped_props {
					prop_name := wrapped_prop[1]
					prop_value := vm.Get(prop_name).String()
					passed_data[prop_name] = prop_value
				}
				renderedComp := Render(component.Content, passed_data)
				content = reComponent.ReplaceAllString(content, renderedComp)
			}
		}
	}

	return content
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
				compContent, err := os.ReadFile(compPath)
				if err != nil {
					log.Fatal(err)
				}
				components = append(components, Component{
					Name:    compName,
					Path:    compPath,
					Content: string(compContent),
				})
				script = reImport.ReplaceAllString(script, "") // Remove current import so script can run in goja
			}
		}
		content = reFence.ReplaceAllString(content, "") // Remove fence entirely
	}
	return content, components, script
}

func main() {
	// Define a template
	templateSrc, _ := os.ReadFile("views/home.html")
	// Render the template with data
	data := map[string]any{"name": "John", "age": 25}
	rendered := Render(string(templateSrc), data)

	fmt.Println(rendered)
}
