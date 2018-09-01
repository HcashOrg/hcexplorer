package internal

const (
	CreateFeesStatTable = `CREATE TABLE IF NOT EXISTS fees_stat (
		id SERIAL PRIMARY KEY,
		time INT8,
		fees INT8,
		rewards INT8,
		size INT8,
		fees_rewards FLOAT8,
		fees_perkb FLOAT8
	);`

	RetrieveRangeBlockHeightOneDay = `SELECT SUM(size), MIN(height), MAX(height) FROM blocks WHERE time>=$1 and time<$2;`
	RetrieveRewardsFeesOneDay = `SELECT SUM(CASE WHEN tx_type = 0 AND block_index = 0 THEN sent ELSE 0 END) rewards, 
	SUM(CASE WHEN fees > 0 THEN fees ELSE 0 END) fees FROM transactions WHERE block_height>=$1 AND block_height<=$2;`

	InsertFeesStat = `INSERT INTO fees_stat (time, fees, rewards, size, fees_rewards, fees_perkb)
		VALUES ($1, $2, $3, $4, $5, $6);`

	RetrieveLast90FeesStat = `SELECT time, fees, fees_rewards, fees_perkb FROM fees_stat ORDER BY time LIMIT 90;`
)