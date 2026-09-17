package order

import (
	"errors"

	"go-crud/internal/user"
)

var (
	ErrValidation    = errors.New("invalid order")
	ErrInvalidStatus = errors.New("invalid status")
)

var validStatuses = map[string]bool{"pending": true, "paid": true, "shipped": true, "cancelled": true}

type Service struct {
	repo     *Repository
	userRepo *user.Repository
}

func NewService(repo *Repository, userRepo *user.Repository) *Service {
	return &Service{repo: repo, userRepo: userRepo}
}

func (s *Service) Create(userID int64, items []ItemInput) (*Order, error) {
	if userID == 0 || len(items) == 0 {
		return nil, ErrValidation
	}
	for _, it := range items {
		if it.ProductID == 0 || it.Quantity <= 0 {
			return nil, ErrValidation
		}
	}
	if _, err := s.userRepo.GetByID(userID); err != nil {
		return nil, err
	}
	return s.repo.Create(userID, items)
}

func (s *Service) Get(id int64) (*Order, error) {
	return s.repo.GetByID(id)
}

func (s *Service) List() ([]Order, error) {
	return s.repo.List()
}

func (s *Service) ListByUser(userID int64) ([]Order, error) {
	return s.repo.ListByUser(userID)
}

func (s *Service) UpdateStatus(id int64, status string) (*Order, error) {
	if !validStatuses[status] {
		return nil, ErrInvalidStatus
	}
	if err := s.repo.UpdateStatus(id, status); err != nil {
		return nil, err
	}
	return s.repo.GetByID(id)
}

func (s *Service) Delete(id int64) error {
	return s.repo.Delete(id)
}
