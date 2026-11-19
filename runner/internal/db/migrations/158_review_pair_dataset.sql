ALTER TABLE live_dataset_attempts DROP CONSTRAINT live_dataset_attempts_source_check;
ALTER TABLE live_dataset_attempts ADD CONSTRAINT live_dataset_attempts_source_check CHECK(source IN
 ('blerg.workers','blerg.engine.usage','blerg.cards.merged','blerg.gates.durations','blerg.cards.time','blerg.reviews.pairs'));
