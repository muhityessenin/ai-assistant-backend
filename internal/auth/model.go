package auth

import "github.com/google/uuid"

type Actor struct {
	UserID         uuid.UUID `json:"user_id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Email          string    `json:"email"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
	CanTrain       bool      `json:"can_train"`
}

func (a Actor) IsAdmin() bool           { return a.Role == "owner" || a.Role == "admin" }
func (a Actor) IsOwner() bool           { return a.Role == "owner" }
func (a Actor) CanTrainAssistant() bool { return a.IsAdmin() || a.CanTrain }
