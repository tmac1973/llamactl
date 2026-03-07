package web

import "embed"

//go:embed templates/*.html templates/partials/*.html
var Templates embed.FS

//go:embed static/*
var Static embed.FS
