package validate

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"imp/internal/apperror"
)

type Validator struct {
	v *validator.Validate
}

func New() *Validator {
	v := validator.New(validator.WithRequiredStructEnabled())

	// Use the json tag name in error keys so they line up with what the client sent.
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		tag := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if tag == "" || tag == "-" {
			return fld.Name
		}
		return tag
	})

	return &Validator{v: v}
}

// Struct runs validation and returns an apperror.Validation with per-field
// messages, or nil if the input is valid.
func (val *Validator) Struct(s any) error {
	if err := val.v.Struct(s); err != nil {
		var verrs validator.ValidationErrors
		if errors.As(err, &verrs) {
			fields := make(map[string]string, len(verrs))
			for _, fe := range verrs {
				fields[fe.Field()] = message(fe)
			}
			return apperror.Validation(fields)
		}
		return apperror.BadRequest(err.Error())
	}
	return nil
}

func message(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return fmt.Sprintf("must be at least %s characters", fe.Param())
	case "max":
		return fmt.Sprintf("must be at most %s characters", fe.Param())
	case "oneof":
		return fmt.Sprintf("must be one of: %s", fe.Param())
	default:
		return fmt.Sprintf("failed %q validation", fe.Tag())
	}
}
