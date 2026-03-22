package driven

import "time"

// SystemClock implements port.ClockProvider using the real system clock.
// Exists solely to satisfy the Clock interface in production wiring —
// tests inject a controlled clock instead.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }
