// File: props_test.go
package bollywood

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type dummyActor struct{}

func (d *dummyActor) Receive(ctx Context) {}

func TestProps_NewProps(t *testing.T) {
	producer := func() Actor { return &dummyActor{} }
	props := NewProps(producer)
	assert.NotNil(t, props)
	assert.NotNil(t, props.producer)

	// Test nil producer panic
	assert.PanicsWithValue(t, "bollywood: producer cannot be nil", func() {
		NewProps(nil)
	}, "NewProps(nil) should panic")
}

func TestProps_Produce(t *testing.T) {
	producer := func() Actor { return &dummyActor{} }
	props := NewProps(producer)
	actorInstance := props.Produce()
	assert.NotNil(t, actorInstance)
	assert.IsType(t, &dummyActor{}, actorInstance)
}
