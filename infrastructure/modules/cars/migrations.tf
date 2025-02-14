module "cars_migration" {
  source = "../cloudrun-job"

  name           = "migration-cars"
  short_name     = "mc"
  region         = var.region
  project        = var.project
  project_number = var.project_number

  env = {
    DB_USER = google_sql_user.cars_downloader.name
    DB_PASS = google_sql_user.cars_downloader.password
    DB_NAME = google_sql_database.database.name
    DB_HOST = "/cloudsql/${var.db_connection_name}"
  }

  run_after_deploy = true
  image            = "europe-west1-docker.pkg.dev/sharedtelemetryapp/sessions-downloader/cars-models:latest"
  args             = ["migrate", "apply", "--url", "postgres://${google_sql_user.cars_downloader.name}:${google_sql_user.cars_downloader.password}@/${google_sql_database.database.name}?host=/cloudsql/${var.db_connection_name}"]

  db_connection_name = var.db_connection_name
}
