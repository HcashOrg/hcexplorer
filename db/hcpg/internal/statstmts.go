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
	RetrieveRewardsFeesOneDay      = `SELECT SUM(CASE WHEN tx_type = 0 AND block_index = 0 THEN sent ELSE 0 END) rewards, 
	SUM(CASE WHEN fees > 0 THEN fees ELSE 0 END) fees FROM transactions WHERE block_height>=$1 AND block_height<=$2;`

	InsertFeesStat = `INSERT INTO fees_stat (time, fees, rewards, size, fees_rewards, fees_perkb)
		VALUES ($1, $2, $3, $4, $5, $6);`

	RetrieveLast90FeesStat = `SELECT time, fees, fees_rewards, fees_perkb FROM fees_stat ORDER BY time LIMIT 90;`

	CreateMempoolHistory = `CREATE TABLE IF NOT EXISTS mempool_history (
		id SERIAL PRIMARY KEY,
		time INT8, -- UNIQUE
 	 	size INT8,
 	 	bytes INT8,
 	 	open INT8,
 	 	close INT8,
 	 	high INT8,
 	 	low INT8,
 	 	types INT8,
 	 	is_day BOOLEAN
	);`
	CreateNodesTable = `create table if not exists nodes (
		id serial primary key,
		ip text not null unique,
		country text	
	);`

	InsertMempoolHistory = `INSERT INTO mempool_history (time, size, bytes) VALUES ($1, $2, $3);`

	RetrieveLastTwoDaysMempoolHistory = `SELECT time, size, bytes FROM mempool_history WHERE time > $1 ORDER BY time;`

	RetrieveMempoolHistoryKline        = `SELECT time, open, close, high, low FROM mempool_history WHERE is_day = true ORDER BY time;`
	RetrieveMempoolHistoryMinMaxOneDay = `SELECT MIN(bytes), MAX(bytes) FROM mempool_history WHERE time>=$1 AND time<$2;`
	RetrieveMempoolHistoryOpenOneDay   = `SELECT bytes FROM mempool_history WHERE time>=$1 AND time<$2 ORDER BY time LIMIT 1;`
	RetrieveMempoolHistoryCloseOneDay  = `SELECT bytes FROM mempool_history WHERE time>=$1 AND time<$2 ORDER BY time DESC LIMIT 1;`

	RetrieveMempoolHistoryTimeExist = `SELECT count(1) FROM mempool_history WHERE time = $1;`
	InsertMempoolHistoryKline       = `INSERT INTO mempool_history (open, close, high, low, time, is_day) VALUES ($1, $2, $3, $4, $5, true);`
	UpdateMempoolHistoryKline       = `UPDATE mempool_history SET open=$1, close=$2, high=$3, low=$4, is_day = true WHERE time = $5;`
)
