package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

var methods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

func main() {
	if len(os.Args) != 2 {
		fail("usage: check-openapi <file>")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err.Error())
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		fail(err.Error())
	}
	if version, _ := spec["openapi"].(string); !strings.HasPrefix(version, "3.1.") {
		fail("OpenAPI version must be 3.1.x")
	}
	paths := object(spec["paths"], "paths")
	operationIDs := map[string]string{}
	for path, rawPath := range paths {
		for method, rawOperation := range object(rawPath, path) {
			if !methods[method] {
				continue
			}
			location := strings.ToUpper(method) + " " + path
			operation := object(rawOperation, location)
			operationID, _ := operation["operationId"].(string)
			if operationID == "" {
				fail(location + " has no operationId")
			}
			if previous := operationIDs[operationID]; previous != "" {
				fail(operationID + " is duplicated at " + previous + " and " + location)
			}
			operationIDs[operationID] = location
			responses := object(operation["responses"], location+" responses")
			requireResponse(responses, location, "default")
			requireResponse(responses, location, "503")
			if !hasSuccess(responses) {
				fail(location + " has no success response")
			}
			if _, hasBody := operation["requestBody"]; hasBody {
				for _, status := range []string{"400", "413", "415"} {
					requireResponse(responses, location, status)
				}
			}
			if protected(operation) {
				requireResponse(responses, location, "401")
			}
			if method != "get" {
				requireResponse(responses, location, "403")
			}
			if hasRevision(operation["parameters"]) {
				requireResponse(responses, location, "428")
			}
		}
	}
	walkRefs(spec, spec)
}

func object(value any, location string) map[string]any {
	result, ok := value.(map[string]any)
	if !ok {
		fail(location + " must be an object")
	}
	return result
}

func protected(operation map[string]any) bool {
	security, overridden := operation["security"]
	if !overridden {
		return true
	}
	items, ok := security.([]any)
	return !ok || len(items) != 0
}

func hasRevision(value any) bool {
	parameters, _ := value.([]any)
	for _, raw := range parameters {
		parameter, _ := raw.(map[string]any)
		if ref, _ := parameter["$ref"].(string); strings.HasSuffix(ref, "/Revision") {
			return true
		}
	}
	return false
}

func hasSuccess(responses map[string]any) bool {
	for status := range responses {
		if len(status) == 3 && status[0] == '2' {
			return true
		}
	}
	return false
}

func requireResponse(responses map[string]any, location, status string) {
	if _, ok := responses[status]; !ok {
		fail(location + " is missing response " + status)
	}
}

func walkRefs(value any, root map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			resolveRef(root, ref)
		}
		for _, child := range typed {
			walkRefs(child, root)
		}
	case []any:
		for _, child := range typed {
			walkRefs(child, root)
		}
	}
}

func resolveRef(root map[string]any, ref string) {
	if !strings.HasPrefix(ref, "#/") {
		fail("external reference is not allowed: " + ref)
	}
	var current any = root
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		current = object(current, ref)[segment]
		if current == nil {
			fail("unresolved reference: " + ref)
		}
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "OpenAPI validation:", message)
	os.Exit(1)
}
