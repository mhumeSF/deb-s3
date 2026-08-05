package apt

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	dependencyPattern      = regexp.MustCompile(`^([^ ]+)(?: \(([>=<]+) ([^)]+)\))?$`)
	dependencyNamePattern  = regexp.MustCompile(`^[^ \(]+`)
	pessimisticPattern     = regexp.MustCompile(`\(~>`)
	inequalityPattern      = regexp.MustCompile(`^(\S+)\s+\(!= (.+)\)$`)
	exactDependencyPattern = regexp.MustCompile(`^(\S+)\s+\(= (.+)\)$`)
)

func ParseDependencies(value string) []string {
	if value == "" {
		return nil
	}
	parts := regexp.MustCompile(`, *`).Split(value, -1)
	dependencies := make([]string, 0, len(parts))
	for _, dependency := range parts {
		match := dependencyPattern.FindStringSubmatch(dependency)
		if match == nil {
			dependencies = append(dependencies, dependency)
			continue
		}
		name, operator, version := match[1], match[2], match[3]
		if operator != "" && version != "" {
			dependencies = append(dependencies, strings.TrimSpace(name+" ("+operator+" "+version+")"))
		} else {
			dependencies = append(dependencies, strings.TrimSpace(name))
		}
	}
	return dependencies
}

func normalizeDependency(dependency string, ignoreIteration bool) (normalized []string, conflict string) {
	if !strings.ContainsAny(dependency, "(,|") {
		parts := strings.Fields(dependency)
		if len(parts) >= 3 {
			dependency = parts[0] + " (" + debianizeOperator(parts[1]) + " " + parts[2] + ")"
		}
	}

	if name := dependencyNamePattern.FindString(dependency); name != "" && strings.IndexFunc(name, func(r rune) bool {
		return r >= 'A' && r <= 'Z'
	}) >= 0 {
		dependency = strings.ToLower(name) + strings.TrimPrefix(dependency, name)
	}
	dependency = strings.ReplaceAll(dependency, "_", "-")

	if pessimisticPattern.MatchString(dependency) {
		cleaned := strings.NewReplacer("(", "", ")", "", "~", "", ">", "").Replace(dependency)
		parts := strings.Fields(cleaned)
		if len(parts) >= 2 {
			next := numericVersion(parts[1])
			if len(next) > 0 {
				increment := len(next) - 2
				if increment < 0 {
					increment = 0
				}
				next[increment]++
				next[len(next)-1] = 0
			}
			return []string{parts[0] + " (>= " + parts[1] + ")", parts[0] + " (<< " + joinNumericVersion(next) + ")"}, ""
		}
	}
	if match := inequalityPattern.FindStringSubmatch(dependency); match != nil {
		return nil, strings.Replace(dependency, "!=", "=", 1)
	}
	if ignoreIteration {
		if match := exactDependencyPattern.FindStringSubmatch(dependency); match != nil {
			next := numericVersion(match[2])
			if len(next) > 0 {
				next[len(next)-1]++
			}
			return []string{match[1] + " (>= " + match[2] + ")", match[1] + " (<< " + joinNumericVersion(next) + ")"}, ""
		}
	}
	return []string{strings.TrimRight(dependency, " \t\r\n")}, ""
}

func debianizeOperator(operator string) string {
	switch operator {
	case "<":
		return "<<"
	case ">":
		return ">>"
	default:
		return operator
	}
}

// numericVersion treats non-numeric segments as 0, so constraint expansion
// (pessimistic and exact-match bounds) is only meaningful for purely numeric
// versions; suffixed versions like "1.2~rc1" expand to approximate bounds.
func numericVersion(version string) []int {
	parts := strings.Split(version, ".")
	numbers := make([]int, len(parts))
	for position, part := range parts {
		numbers[position], _ = strconv.Atoi(part)
	}
	return numbers
}

func joinNumericVersion(version []int) string {
	parts := make([]string, len(version))
	for position, part := range version {
		parts[position] = strconv.Itoa(part)
	}
	return strings.Join(parts, ".")
}
