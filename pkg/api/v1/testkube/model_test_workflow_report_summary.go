/*
 * Testkube API
 *
 * Testkube provides a Kubernetes-native framework for test definition, execution and results
 *
 * API version: 1.0.0
 * Contact: testkube@kubeshop.io
 * Generated by: Swagger Codegen (https://github.com/swagger-api/swagger-codegen.git)
 */
package testkube

type TestWorkflowReportSummary struct {
	// total number of test cases
	Tests int32 `json:"tests,omitempty"`
	// number of passed test cases
	Passed int32 `json:"passed,omitempty"`
	// number of failed test cases
	Failed int32 `json:"failed,omitempty"`
	// number of skipped test cases
	Skipped int32 `json:"skipped,omitempty"`
	// number of error test cases
	Errored int32 `json:"errored,omitempty"`
	// total duration of all test cases in milliseconds
	Duration int64 `json:"duration,omitempty"`
}
