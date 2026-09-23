package setup

import (
	"strconv"

	"github.com/a-h/templ"
)

type templComponent = templ.Component

func itoa(n int) string { return strconv.Itoa(n) }
