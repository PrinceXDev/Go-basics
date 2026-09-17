package product

import "errors"

var ErrValidation = errors.New("invalid product")

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(name string, price float64, stock int) (*Product, error) {
	if name == "" || price < 0 || stock < 0 {
		return nil, ErrValidation
	}
	p := &Product{Name: name, Price: price, Stock: stock}
	if err := s.repo.Create(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) Get(id int64) (*Product, error) {
	return s.repo.GetByID(id)
}

func (s *Service) List() ([]Product, error) {
	return s.repo.List()
}

func (s *Service) Update(id int64, name string, price float64, stock int) (*Product, error) {
	if name == "" || price < 0 || stock < 0 {
		return nil, ErrValidation
	}
	p := &Product{ID: id, Name: name, Price: price, Stock: stock}
	if err := s.repo.Update(p); err != nil {
		return nil, err
	}
	return s.repo.GetByID(id)
}

func (s *Service) Delete(id int64) error {
	return s.repo.Delete(id)
}
