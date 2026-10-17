package billing

import "fmt"

type Mode string

const (
	ModeLegacy   Mode = "legacy"
	ModeDisabled Mode = "disabled"
	ModeStrict   Mode = "strict"
)

func (m Mode) Validate() error {
	switch m {
	case ModeLegacy, ModeDisabled, ModeStrict:
		return nil
	default:
		return fmt.Errorf("billing.mode must be legacy, disabled or strict")
	}
}

// A partial deployment must never fall back to legacy charging.
func (m Mode) CheckAvailable(strictReady bool) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m == ModeStrict && !strictReady {
		return fmt.Errorf("strict billing is unavailable: durable budget settlement and recovery are not connected")
	}
	return nil
}
