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

	// Replace placeholders with data
	for key, value := range data {
		switch value := value.(type) {
		case string:
			content = strings.ReplaceAll(content, "{"+key+"}", value)
		case int:
			content = strings.ReplaceAll(content, "{"+key+"}", strconv.Itoa(value))
		default:
			// handle other values
		}
	}

	// Recursively render imports
	for _, component := range components {
		re := regexp.MustCompile(fmt.Sprintf(`<%s(.*?)/>`, component.Name))
		match := re.FindStringSubmatch(content)
		if len(match) > 1 {
			renderedComp := Render(component.Content, data)
			content = re.ReplaceAllString(content, renderedComp)
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
