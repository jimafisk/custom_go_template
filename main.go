package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Component struct {
	Name    string
	Path    string
	Content string
}

// Render renders the template with the given data
func Render(content string, data map[string]any) string {

	// Get list of imported components
	components := getComponents(content)

	// Remove fences
	re := regexp.MustCompile(`(?s)---(.*?)---`)
	content = re.ReplaceAllString(content, "")

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
					passed_data[prop_name] = data[prop_name] // TODO: Actually need to evaluate value
				}
				renderedComp := Render(component.Content, passed_data)
				content = reComponent.ReplaceAllString(content, renderedComp)
			}
		}
	}

	// Replace placeholders with data
	for name, value := range data {
		switch value := value.(type) {
		case string:
			content = strings.ReplaceAll(content, "{"+name+"}", value)
		case int:
			content = strings.ReplaceAll(content, "{"+name+"}", strconv.Itoa(value))
		default:
			// handle other values
		}
	}

	return content
}

func getComponents(content string) []Component {
	components := []Component{}
	re := regexp.MustCompile(`(?s)---(.*?)---`)
	matches := re.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		for _, line := range strings.Split(match[1], "\n") {
			re := regexp.MustCompile(`import\s+([A-Za-z_][A-Za-z_0-9]*)\s+from\s*"([^"]+)`)
			match := re.FindStringSubmatch(line)
			if len(match) > 1 {
				name := match[1]
				path := match[2]
				content, err := os.ReadFile(path)
				if err != nil {
					log.Fatal(err)
				}
				components = append(components, Component{
					Name:    name,
					Path:    path,
					Content: string(content),
				})
			}
		}
	}
	return components
}

func main() {
	// Define a template
	templateSrc, _ := os.ReadFile("views/home.html")
	// Render the template with data
	data := map[string]any{"name": "John", "age": 25}
	rendered := Render(string(templateSrc), data)

	fmt.Println(rendered)
}
