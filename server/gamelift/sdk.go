package gamelift

import "github.com/amazon-gamelift/amazon-gamelift-servers-go-server-sdk/v5/server"

// SDK is the Platform backed by the real Amazon GameLift Servers server SDK. Tests use a fake Platform
// instead, so the lifecycle logic runs without the service.
type SDK struct{}

func (SDK) ActivateGameSession() error { return server.ActivateGameSession() }

func (SDK) AcceptPlayerSession(id string) error { return server.AcceptPlayerSession(id) }

func (SDK) RemovePlayerSession(id string) error { return server.RemovePlayerSession(id) }

func (SDK) ProcessEnding() error { return server.ProcessEnding() }
