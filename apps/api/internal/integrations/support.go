package integrations

import (
	"crypto/rand"
	"fmt"
	"time"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type randomIDGenerator struct{}

func (randomIDGenerator) NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func clockOrDefault(clock Clock) Clock {
	if clock == nil {
		return systemClock{}
	}
	return clock
}

func idGeneratorOrDefault(generator IDGenerator) IDGenerator {
	if generator == nil {
		return randomIDGenerator{}
	}
	return generator
}

func copyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func copyBytes(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}
	return append([]byte(nil), value...)
}

func copyStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
