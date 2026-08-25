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
		// IPs or CIDRs of reverse proxies whose X-Forwarded-For header is
		// trusted for rate-limiting purposes. Empty (the default) disables
		// X-Forwarded-For handling entirely.
		TrustedProxies []string `yaml:"trusted_proxies"`
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
