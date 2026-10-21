module github.com/chicohaager/modmanager

go 1.27.0

toolchain go1.27.3

require github.com/chicohaager/lintux-modkit v0.0.0

// Until lintux-modkit is published, build against the sibling checkout.
replace github.com/chicohaager/lintux-modkit => ../lintux-modkit
