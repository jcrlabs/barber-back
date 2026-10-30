package handlers

import (
	"regexp"

	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

var phoneRe = regexp.MustCompile(`^\+?[0-9 ()\-]{6,30}$`)

// RegisterValidators añade validaciones propias y obliga a rechazar campos JSON desconocidos.
func RegisterValidators() {
	binding.EnableDecoderDisallowUnknownFields = true
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		_ = v.RegisterValidation("phone", func(fl validator.FieldLevel) bool {
			return phoneRe.MatchString(fl.Field().String())
		})
	}
}
