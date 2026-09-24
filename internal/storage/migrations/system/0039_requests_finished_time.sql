CREATE INDEX requests_finished ON requests(finished_at DESC, id DESC) WHERE finished_at IS NOT NULL;
