package pipeline

import (
	"github.com/majiayu000/auto-contributor/pkg/logger"
	"github.com/sirupsen/logrus"
)

var log = logger.GetLogger()

// Fields is a re-export of logrus.Fields for use within the pipeline package.
type Fields = logrus.Fields
