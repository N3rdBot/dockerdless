package ports

import "github.com/N3rdBot/dockerdless/internal/domain"

// The compile-time assertion lives with the contract it guards: domain must not
// import its own ports, so the proof that Registry satisfies State belongs
// here, next to the interface.
var _ State = (*domain.Registry)(nil)
