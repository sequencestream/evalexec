// A separate module on purpose.
//
// An examples/ directory inside the main module only proves the code compiles.
// It cannot prove that everything a downstream program needs is actually
// exported — a required type left behind a lowercase identifier compiles fine
// from inside the module and fails from outside it. Only a cross-module build
// catches that, which is why this has its own go.mod and its own CI step.
module example.com/evalexec-consumer

go 1.26

require github.com/sequencestream/evalexec v0.0.0

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/vogo/aimodel v0.5.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)

replace github.com/sequencestream/evalexec => ../..
