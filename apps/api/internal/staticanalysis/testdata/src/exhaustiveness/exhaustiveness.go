package exhaustiveness

type policyState uint8

const (
	policyStatePending policyState = iota
	policyStateActive
	policyStateDone
)

func describePolicyState(state policyState) string {
	switch state {
	case policyStatePending:
		return "pending"
	case policyStateActive:
		return "active"
	}
	return "unknown"
}

//sumtype:decl
type policyEvent interface {
	policyEvent()
}

type createdEvent struct{}

func (*createdEvent) policyEvent() {}

type deletedEvent struct{}

func (*deletedEvent) policyEvent() {}

func describePolicyEvent(event policyEvent) string {
	switch event.(type) {
	case *createdEvent:
		return "created"
	}
	return "unknown"
}
