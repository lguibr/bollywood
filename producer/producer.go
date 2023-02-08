package producer

import (
	"sync"

	"github.com/lguibr/bollywood/actor"
)

var once sync.Once

type Producer struct {
	Actor      *actor.Actor
	Production map[string]func(interface{})
}

func NewProducer(actor *actor.Actor, production map[string]func(interface{})) *Producer {
	return &Producer{
		Actor:      actor,
		Production: production,
	}
}

func (p *Producer) Produce() {
	p.Actor.Mailbox.Activated = true
	go func() {
		for id, f := range p.Production {
			if !p.Actor.Mailbox.Activated {
				return
			}
			address := p.Actor.Mailbox.Get(id)
			f(address.Receive())
			once.Do(p.Actor.Mailbox.OpenAll)
		}
	}()
}
