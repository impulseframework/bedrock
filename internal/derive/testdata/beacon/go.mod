module github.com/impulseframework/beacon

go 1.27.3

require (
	cloud.google.com/go/logging v1.19.1
	cloud.google.com/go/spanner v1.95.0
	github.com/go-chi/chi/v5 v5.3.2
	github.com/go-playground/errors/v5 v5.4.0
	github.com/go-playground/validator/v10 v10.30.3
	github.com/impulseframework/impulse v0.1.0
	github.com/jtwatson/server v0.2.2
	github.com/jtwatson/spaassets v0.0.0-20160917192555-583a733b0a63
	github.com/sethvargo/go-envconfig v1.4.3
)

tool github.com/impulseframework/impulse/cmd/impulse
