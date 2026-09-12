package sshhost

// RouteEffective returns independent native effective-config snapshots for a
// route produced by this service, in the same order as Route.Hops. Values are
// for local policy classification only and must never be serialized wholesale.
func (s *Service) RouteEffective(route Route) ([]EffectiveConfig, error) {
	state, err := s.validateRoute(route)
	if err != nil {
		return nil, err
	}
	result := make([]EffectiveConfig, len(state.hops))
	for index, hop := range state.hops {
		result[index] = cloneEffective(hop.effective)
	}
	return result, nil
}
