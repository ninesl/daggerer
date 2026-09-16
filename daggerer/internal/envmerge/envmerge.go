package envmerge

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/joho/godotenv"
)

type Variable struct {
	Name  string
	Value string
}

var shellVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func Parse(contents string) ([]Variable, error) {
	values, err := godotenv.Unmarshal(contents)
	if err != nil {
		return nil, err
	}
	variables := make([]Variable, 0, len(values))
	for name, value := range values {
		variables = append(variables, Variable{Name: name, Value: value})
	}
	return Merge(variables), nil
}

func Merge(sources ...[]Variable) []Variable {
	values := map[string]string{}
	for _, source := range sources {
		for _, variable := range source {
			values[variable.Name] = variable.Value
		}
	}
	if len(values) == 0 {
		return nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	merged := make([]Variable, 0, len(names))
	for _, name := range names {
		merged = append(merged, Variable{Name: name, Value: values[name]})
	}
	return merged
}

func Collision(public, private []Variable) (string, bool) {
	publicNames := make(map[string]struct{}, len(public))
	for _, variable := range public {
		publicNames[variable.Name] = struct{}{}
	}
	for _, variable := range private {
		if _, exists := publicNames[variable.Name]; exists {
			return variable.Name, true
		}
	}
	return "", false
}

func ShellDotenv(values []Variable) (string, error) {
	return shellAssignments(values, "")
}

func ShellExports(values []Variable) (string, error) {
	return shellAssignments(values, "export ")
}

func shellAssignments(values []Variable, prefix string) (string, error) {
	var contents strings.Builder
	for _, variable := range values {
		if !shellVariableName.MatchString(variable.Name) {
			return "", fmt.Errorf("private variable %q is not a valid shell variable name", variable.Name)
		}
		if strings.ContainsRune(variable.Value, 0) {
			return "", fmt.Errorf("private variable %q contains a NUL byte", variable.Name)
		}
		value := strings.ReplaceAll(variable.Value, "'", `'"'"'`)
		fmt.Fprintf(&contents, "%s%s='%s'\n", prefix, variable.Name, value)
	}
	return contents.String(), nil
}
