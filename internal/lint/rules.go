package lint

// ruleGuide says what a rule finds and how to fix its findings. Output shows
// it once above the findings of that rule, so each finding message carries
// only what differs between findings.
type ruleGuide struct {
	finds string
	fix   string
}

const (
	sizeFixFirst = "First check what each part is for and who calls it, and delete what nothing needs: legacy paths, code only tests use, redundant checks, defensive code for cases that cannot happen. "
	reviewFix    = "Apply the advice on each line. Code review findings cannot be ignored."
	groupingFix  = "Split related declarations into groups with blank lines or group comments."
	redundantFix = "Delete the redundant code; the evidence shows the paths that already decide it."
	staleFix     = "Delete the directive."
	resultFix    = "Return ordinary results: a value and an error, or a named struct."
)

var ruleGuides = map[string]ruleGuide{
	"abstraction_overkill": {
		"Private interfaces with one implementation",
		"Use the concrete type unless code really swaps implementations.",
	},
	"api_overkill": {
		"Private APIs more flexible than their callers need",
		"Pass config or values directly, and remove parameters every caller sets the same way.",
	},
	"append_ceremony": {
		"Guards around append that append already handles",
		"Delete the guard.",
	},
	"behavior_clone": {
		"Functions or blocks with the same behavior",
		"Merge the copies into one implementation that every caller uses.",
	},
	"bool_mode_param": {
		"Private functions switched by a literal bool",
		"Split into named functions, or use a typed mode.",
	},
	"boolean_ceremony": {
		"Ifs that only produce true or false",
		"Use the boolean expression the finding shows.",
	},
	"comment_crutch": {"Comments that explain unclear code", reviewFix},
	"comment_excuse": {
		"Comments and suppressions that excuse code instead of fixing it",
		reviewFix,
	},
	"comment_noise": {
		"Comments that restate the code",
		"Delete the comment, or rename so the code says it. " + reviewFix,
	},
	"complexity_simplification": {
		"Functions with branches or guards that can go",
		"Remove the redundant branches and guards before adding control flow.",
	},
	"const_grouping": {"Long const blocks", groupingFix},
	"const_value_test": {
		"Tests that only check constants",
		"Delete the test, or test behavior that uses the value.",
	},
	"control_flow_merge": {
		"Guard returns that repeat the next return",
		"Delete the redundant guards.",
	},
	"control_flow_pyramid": {
		"Nested ifs at the end of a function",
		"Turn the conditions into early returns at the top.",
	},
	"dead_code": {
		"Declarations nothing reachable uses",
		"Delete them, with the code and tests that only exist for them.",
	},
	"generic_naming": {
		"Private helpers with generic names",
		"Rename the helper for what it does, or inline it.",
	},
	"god_receiver": {
		"Types with over 40 methods, counting functions that take *T first and methods of types only they embed",
		sizeFixFirst + "Then simplify methods more complex than their job, merge methods that differ without need, move methods another type owns, and split the rest by responsibility so each new type owns its own state. Moving methods into an embedded type, or into functions that take the type, is not a fix.",
	},
	"guard_complexity": {
		"Input guards mixed with other work",
		"Keep the validation guards together at the top, or split the function.",
	},
	"helper_fragment": {
		"Private helpers that are a piece of their only caller",
		reviewFix,
	},
	"invalid_contract": {
		"Malformed slopelint:ensures directives",
		"Write `<param>[.<field>...] ==|!= <nil|true|false|\"string\"|int>`.",
	},
	"invalid_ignore": {
		"Malformed slopelint:ignore directives",
		"Write `//slopelint:ignore <rule> -- <reason>` for a rule that is not a code review rule.",
	},
	"long_function": {
		"Functions over 40 statements; closures inside count, and a switch counts one per case plus its longest case",
		sizeFixFirst + "Then give each separate job its own function, starting with the largest statements. Cutting a function into steps that each have one caller only moves the code.",
	},
	"long_param_list": {
		"Functions with over 6 parameters",
		sizeFixFirst + "Then group related parameters into a struct, or split the function.",
	},
	"long_result_list": {
		"Functions with over 3 results",
		sizeFixFirst + "Then return a named struct.",
	},
	"loop_external_call": {
		"Network or database calls inside loops",
		"Batch the requests, or move them out of the loop.",
	},
	"loop_invariant_work": {
		"Work inside loops that does not depend on the loop",
		"Move the work out of the loop, or build the lookup once before it.",
	},
	"loop_membership_scan": {
		"Membership scans inside loops",
		"Build a set before the loop.",
	},
	"loop_sort": {
		"Sorts inside loops",
		"Sort once outside the loop, or keep the order as you insert.",
	},
	"mixed_const_prefixes": {"Const blocks that mix unrelated families", groupingFix},
	"nested_lookup_loop": {
		"Nested loops that look items up",
		"Build a lookup map before the outer loop.",
	},
	"normalization_ceremony": {
		"Normalization computed more than once",
		"Bind the normalized value once.",
	},
	"optional_result_triple": {"Functions that return (value, ok, error)", resultFix},
	"oversized_owner_file": {
		"Files over 1,000 lines",
		sizeFixFirst + "Then split the rest by responsibility so the whole file can be reviewed.",
	},
	"pairwise_comparison_loop": {
		"Nested loops that compare every pair",
		"Sort with two pointers, sweep, or bucket the inputs.",
	},
	parseErrorKind: {
		"Files the parser cannot read",
		"Other rules skip these files. If the code is valid, the tree-sitter grammar has a bug.",
	},
	"predicate_signature": {
		"Is-functions that do not return bool",
		"Change the signature as the finding says.",
	},
	"prod_must_panic": {
		"Production code that panics on an error",
		"Return the error, or prove the invariant before the call.",
	},
	"range_ceremony": {
		"Guards around range that range already handles",
		"Delete the guard.",
	},
	"redundant_condition":     {"Conditions that earlier checks already decide", redundantFix},
	"redundant_default":       {"Default cases that cannot run", redundantFix},
	"redundant_subexpression": {"Operands that earlier checks already decide", redundantFix},
	"repeated_test_fixture": {
		"Test fixtures copied between tests",
		"Build the fixture once in the test builder that owns it.",
	},
	"result_wrapper": {
		"Private structs that only wrap a value and a status",
		resultFix,
	},
	"semantic_duplicate": {
		"Functions with similar source or behavior",
		"Merge them into one implementation. A match is a hint, not proof: if the two do different jobs, they can stay.",
	},
	"sentinel_error_break": {
		"Sentinel errors used to stop a callback",
		"Use an explicit stop signal.",
	},
	"serialization_ceremony": {
		"Encoding methods the standard library already covers",
		"Delete the method the finding names.",
	},
	"stale_complexity_suppression": {"Complexity //nolint on code without decisions", staleFix},
	"stale_ignore":                 {"slopelint:ignore directives that no finding uses", staleFix},
	"swallowed_error":              {"Dropped or caught errors that hide failures", reviewFix},
	"table_test_grouping": {
		"Table tests without case names",
		"Name each case so failures say which scenario broke.",
	},
	"temp_alias": {
		"Locals that only rename an expression for one use",
		"Inline the expression.",
	},
	"test_fatal_panic": {
		"Panics after a test's fatal call",
		"Return the zero value instead.",
	},
	"test_focused": {
		"Focused tests that skip the rest of the file",
		"Remove .only, fit, or fdescribe.",
	},
	"test_global_func_stub": {
		"Tests that reassign package function variables",
		"Pass the dependency through the API that owns it.",
	},
	"test_mock_only": {"Tests that only check mocks", reviewFix},
	"test_no_assertion": {
		"Tests that cannot fail",
		"Delete the test, or make it check behavior that can fail.",
	},
	"test_not_parallel": {
		"Tests that run serially or call t.Parallel() twice",
		"Do what the finding says. A serial test needs `//slopelint:serial <reason>` that names the shared state.",
	},
	"test_repeated_steps": {"Tests that should share a table or fixture", reviewFix},
	"test_serial_excuse":  {"Weak //slopelint:serial reasons", reviewFix},
	"test_sleep": {
		"Real sleeps in tests",
		"Wait on the event, channel, or condition, or use a fake clock or timers.",
	},
	"test_support_filename": {
		"Shared test-only files without the test_support marker",
		"Rename the file to include test_support, such as helpers_test_support.go, and keep the .go suffix.",
	},
	"test_trivial":  {"Tests that check nothing real", reviewFix},
	"test_wasteful": {"Tests that waste time", reviewFix},
	"trivial_wrapper": {
		"Helpers that only forward",
		"Inline it at the caller, or merge the names.",
	},
	"type_grouping":    {"Long type blocks", groupingFix},
	"unreachable_case": {"Switch cases that cannot run", redundantFix},
	"unused_private_param": {
		"Private functions with unused parameters",
		"Remove the parameter and update callers." + " Name it _ if the signature must keep it.",
	},
	"var_grouping": {"Long var blocks", groupingFix},
}

// RuleGuide returns what a rule finds and how to fix it; both are empty for
// an unknown rule.
func RuleGuide(kind string) (string, string) {
	guide := ruleGuides[kind]

	return guide.finds, guide.fix
}
