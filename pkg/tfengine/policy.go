package tfengine

import "github.com/itsnotyoutoday/gtfparser"

// policy adapts Options.Policy to gtfparser.Policy.
type policy struct{ f func(op, detail string) error }

func (p policy) Allow(op gtfparser.Op, detail string) error {
	if p.f == nil {
		return nil
	}
	return p.f(op.String(), detail)
}
