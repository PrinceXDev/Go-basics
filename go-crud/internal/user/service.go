package user

import "errors"

var ErrValidation = errors.New("invalid user")

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(name, email string) (*User, error) {
	if name == "" || email == "" {
		return nil, ErrValidation
	}
	u := &User{Name: name, Email: email}
	if err := s.repo.Create(u); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) Get(id int64) (*User, error) {
	return s.repo.GetByID(id)
}

func (s *Service) List() ([]User, error) {
	return s.repo.List()
}

func (s *Service) Update(id int64, name, email string) (*User, error) {
	if name == "" || email == "" {
		return nil, ErrValidation
	}
	u := &User{ID: id, Name: name, Email: email}
	if err := s.repo.Update(u); err != nil {
		return nil, err
	}
	return s.repo.GetByID(id)
}

func (s *Service) Delete(id int64) error {
	return s.repo.Delete(id)
}
