package utils

import "regexp"

var (
	isAlphanumericRegex = regexp.MustCompile(`^[a-zA-Z0-9]*$`).MatchString
)

func StringIsAlphanumeric(s string) bool {
	return isAlphanumericRegex(s)
}
