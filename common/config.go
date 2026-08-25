package common

import "github.com/jinzhu/configor"

type Deployment struct {
	Name        string   `required:"true"`
	Description string   `default:""`
	Secret      string   `required:"true"`
	User        string   `default:"root"`
	CronDeploy  string   `yaml:"cron_deploy"`
	Script      []string `required:"true"`
}

type Config struct {
	API struct {
		IP   string `default:""`
		Port uint   `default:"1337"`
		// Deprecated: API clients now use unnamed Unix sockets.
		Unixsocket string `default:"./service_client.sock"`
	}

	Service struct {
		Unixsocket string `default:"./service.sock"`
	}

	Deployments []Deployment `required:"true"`
}

func MakeConfig(path string) (*Config, error) {
	var config Config

	if err := configor.Load(&config, path); err != nil {
		return nil, err
	}

	return &config, nil
}

func (config *Config) FindDeploymentByName(name string) *Deployment {
	for _, deployment := range config.Deployments {
		if deployment.Name == name {
			return &deployment
		}
	}

	return nil
}
