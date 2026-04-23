package lint

func applyContractSequence[T any](
	initial state,
	contracts []T,
	normalize func([]state) []state,
	apply func(state, T) []state,
) []state {
	if len(contracts) == 0 {
		return []state{initial}
	}

	current := []state{initial}
	for _, contract := range contracts {
		nextStates := make([]state, 0, len(current))
		for _, currentState := range current {
			nextStates = append(nextStates, apply(currentState, contract)...)
		}

		current = normalize(nextStates)
		if len(current) == 0 {
			return nil
		}
	}

	return current
}
