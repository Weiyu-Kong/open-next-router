package web

import _ "embed"

//go:embed index.html
var indexHTML string

//go:embed app.css
var appCSS string

//go:embed app.js
var appJS string

//go:embed user.html
var userHTML string

//go:embed user.css
var userCSS string

//go:embed user.js
var userJS string
