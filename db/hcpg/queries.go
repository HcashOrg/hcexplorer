// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package hcpg

import (
	"bytes"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/HcashOrg/hcd/hcutil"
	"github.com/HcashOrg/hcexplorer/db/dbtypes"
	"github.com/HcashOrg/hcexplorer/db/hcpg/internal"
	"github.com/lib/pq"
)

func RetrievePkScriptByID(db *sql.DB, id uint64) (pkScript []byte, err error) {
	err = db.QueryRow(internal.SelectPkScriptByID, id).Scan(&pkScript)
	return
}

func RetrieveVoutIDByOutpoint(db *sql.DB, txHash string, voutIndex uint32) (id uint64, err error) {
	err = db.QueryRow(internal.SelectVoutIDByOutpoint, txHash, voutIndex).Scan(&id)
	return
}

func SetSpendingForVinDbIDs(db *sql.DB, vinDbIDs []uint64) ([]int64, int64, error) {
	// get funding details for vin and set them in the address table
	dbtx, err := db.Begin()
	if err != nil {
		return nil, 0, fmt.Errorf(`unable to begin database transaction: %v`, err)
	}

	var vinGetStmt *sql.Stmt
	vinGetStmt, err = dbtx.Prepare(internal.SelectAllVinInfoByID)
	if err != nil {
		log.Errorf("Vin SELECT prepare failed: %v", err)
		// Already up a creek. Just return error from Prepare.
		_ = dbtx.Rollback()
		return nil, 0, err
	}

	var addrSetStmt *sql.Stmt
	addrSetStmt, err = dbtx.Prepare(internal.SetAddressSpendingForOutpoint)
	if err != nil {
		log.Errorf("address row UPDATE prepare failed: %v", err)
		// Already up a creek. Just return error from Prepare.
		_ = vinGetStmt.Close()
		_ = dbtx.Rollback()
		return nil, 0, err
	}

	bail := func() error {
		// Already up a creek. Just return error from Prepare.
		_ = vinGetStmt.Close()
		_ = addrSetStmt.Close()
		return dbtx.Rollback()
	}

	addressRowsUpdated := make([]int64, len(vinDbIDs))

	for iv, vinDbID := range vinDbIDs {
		// Get the funding tx outpoint (vins table) for the vin DB ID
		var prevOutHash, txHash string
		var prevOutVoutInd, txVinInd uint32
		var prevOutTree, txTree int8
		var id uint64
		err = vinGetStmt.QueryRow(vinDbID).Scan(&id,
			&txHash, &txVinInd, &txTree,
			&prevOutHash, &prevOutVoutInd, &prevOutTree)
		if err != nil {
			return addressRowsUpdated, 0, fmt.Errorf(`SetSpendingForVinDbIDs: `+
				`%v + %v (rollback)`, err, bail())
		}

		// skip coinbase inputs
		if bytes.Equal(zeroHashStringBytes, []byte(prevOutHash)) {
			continue
		}

		// Set the spending tx info (addresses table) for the vin DB ID
		var res sql.Result
		res, err = addrSetStmt.Exec(prevOutHash, prevOutVoutInd,
			0, txHash, txVinInd, vinDbID)
		if err != nil || res == nil {
			return addressRowsUpdated, 0, fmt.Errorf(`SetSpendingForVinDbIDs: `+
				`%v + %v (rollback)`, err, bail())
		}

		addressRowsUpdated[iv], err = res.RowsAffected()
		if err != nil {
			return addressRowsUpdated, 0, fmt.Errorf(`RowsAffected: `+
				`%v + %v (rollback)`, err, bail())
		}
	}

	// Close prepared statements. Ignore errors as we'll Commit regardless.
	_ = vinGetStmt.Close()
	_ = addrSetStmt.Close()

	var totalUpdated int64
	for _, n := range addressRowsUpdated {
		totalUpdated += n
	}

	return addressRowsUpdated, totalUpdated, dbtx.Commit()
}

func SetSpendingForVinDbID(db *sql.DB, vinDbID uint64) (int64, error) {
	// get funding details for vin and set them in the address table
	dbtx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf(`unable to begin database transaction: %v`, err)
	}

	// Get the funding tx outpoint (vins table) for the vin DB ID
	var prevOutHash, txHash string
	var prevOutVoutInd, txVinInd uint32
	var prevOutTree, txTree int8
	var id uint64
	err = dbtx.QueryRow(internal.SelectAllVinInfoByID, vinDbID).
		Scan(&id, &txHash, &txVinInd, &txTree,
			&prevOutHash, &prevOutVoutInd, &prevOutTree)
	if err != nil {
		return 0, fmt.Errorf(`SetSpendingByVinID: %v + %v `+
			`(rollback)`, err, dbtx.Rollback())
	}

	// skip coinbase inputs
	if bytes.Equal(zeroHashStringBytes, []byte(prevOutHash)) {
		return 0, dbtx.Rollback()
	}

	// Set the spending tx info (addresses table) for the vin DB ID
	var res sql.Result
	res, err = dbtx.Exec(internal.SetAddressSpendingForOutpoint,
		prevOutHash, prevOutVoutInd,
		0, txHash, txVinInd, vinDbID)
	if err != nil || res == nil {
		return 0, fmt.Errorf(`SetSpendingByVinID: %v + %v `+
			`(rollback)`, err, dbtx.Rollback())
	}

	var N int64
	N, err = res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf(`RowsAffected: %v + %v (rollback)`,
			err, dbtx.Rollback())
	}

	return N, dbtx.Commit()
}

func SetSpendingForFundingOP(db *sql.DB,
	fundingTxHash string, fundingTxVoutIndex uint32,
	spendingTxDbID uint64, spendingTxHash string, spendingTxVinIndex uint32,
	vinDbID uint64) (int64, error) {
	res, err := db.Exec(internal.SetAddressSpendingForOutpoint,
		fundingTxHash, fundingTxVoutIndex,
		spendingTxDbID, spendingTxHash, spendingTxVinIndex, vinDbID)
	if err != nil || res == nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetSpendingByVinID is for when you got a new spending tx (vin entry) and you
// need to get the funding (previous output) tx info, and then update the
// corresponding row in the addresses table with the spending tx info.
func SetSpendingByVinID(db *sql.DB, vinDbID uint64, spendingTxDbID uint64,
	spendingTxHash string, spendingTxVinIndex uint32) (int64, error) {
	// get funding details for vin and set them in the address table
	dbtx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf(`unable to begin database transaction: %v`, err)
	}

	// Get the funding tx outpoint (vins table) for the vin DB ID
	var fundingTxHash string
	var fundingTxVoutIndex uint32
	var tree int8
	err = dbtx.QueryRow(internal.SelectFundingOutpointByVinID, vinDbID).
		Scan(&fundingTxHash, &fundingTxVoutIndex, &tree)
	if err != nil {
		return 0, fmt.Errorf(`SetSpendingByVinID: %v + %v `+
			`(rollback)`, err, dbtx.Rollback())
	}

	// skip coinbase inputs
	if bytes.Equal(zeroHashStringBytes, []byte(fundingTxHash)) {
		return 0, dbtx.Rollback()
	}

	// Set the spending tx info (addresses table) for the vin DB ID
	var res sql.Result
	res, err = dbtx.Exec(internal.SetAddressSpendingForOutpoint,
		fundingTxHash, fundingTxVoutIndex,
		spendingTxDbID, spendingTxHash, spendingTxVinIndex, vinDbID)
	if err != nil || res == nil {
		return 0, fmt.Errorf(`SetSpendingByVinID: %v + %v `+
			`(rollback)`, err, dbtx.Rollback())
	}

	var N int64
	N, err = res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf(`RowsAffected: %v + %v (rollback)`,
			err, dbtx.Rollback())
	}

	return N, dbtx.Commit()
}

func SetSpendingForAddressDbID(db *sql.DB, addrDbID uint64, spendingTxDbID uint64,
	spendingTxHash string, spendingTxVinIndex uint32, vinDbID uint64) error {
	_, err := db.Exec(internal.SetAddressSpendingForID, addrDbID, spendingTxDbID,
		spendingTxHash, spendingTxVinIndex, vinDbID)
	return err
}

func RetrieveAddressRecvCount(db *sql.DB, address string) (count int64, err error) {
	err = db.QueryRow(internal.SelectAddressRecvCount, address).Scan(&count)
	return
}

func RetrieveAddressUnspent(db *sql.DB, address string) (count, totalAmount int64, err error) {
	err = db.QueryRow(internal.SelectAddressUnspentCountAndValue, address).
		Scan(&count, &totalAmount)
	return
}

func RetrieveAddressSpent(db *sql.DB, address string) (count, totalAmount int64, err error) {
	err = db.QueryRow(internal.SelectAddressSpentCountAndValue, address).
		Scan(&count, &totalAmount)
	return
}

func RetrieveAddressSpentUnspent(db *sql.DB, address string) (numSpent, numUnspent,
	totalSpent, totalUnspent int64, err error) {
	dbtx, err := db.Begin()
	if err != nil {
		err = fmt.Errorf("unable to begin database transaction: %v", err)
		return
	}

	var nu, tu sql.NullInt64
	err = dbtx.QueryRow(internal.SelectAddressUnspentCountAndValue, address).
		Scan(&nu, &tu)
	if err != nil && err != sql.ErrNoRows {
		if errRoll := dbtx.Rollback(); errRoll != nil {
			log.Errorf("Rollback failed: %v", errRoll)
		}
		err = fmt.Errorf("unable to QueryRow for unspent amount: %v", err)
		return
	}
	numUnspent, totalUnspent = nu.Int64, tu.Int64

	var ns, ts sql.NullInt64
	err = dbtx.QueryRow(internal.SelectAddressSpentCountAndValue, address).
		Scan(&ns, &ts)
	if err != nil && err != sql.ErrNoRows {
		if errRoll := dbtx.Rollback(); errRoll != nil {
			log.Errorf("Rollback failed: %v", errRoll)
		}
		err = fmt.Errorf("unable to QueryRow for spent amount: %v", err)
		return
	}
	numSpent, totalSpent = ns.Int64, ts.Int64

	err = dbtx.Rollback()
	return
}

func RetrieveAllAddressTxns(db *sql.DB, address string) ([]uint64, []*dbtypes.AddressRow, error) {
	rows, err := db.Query(internal.SelectAddressAllByAddress, address)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	return scanAddressQueryRows(rows)
}

func RetrieveAddressTxns(db *sql.DB, address string, N, offset int64) ([]uint64, []*dbtypes.AddressRow, error) {
	return retrieveAddressTxns(db, address, N, offset,
		internal.SelectAddressLimitNByAddressSubQry)
}

func RetrieveAddressTxnsAlt(db *sql.DB, address string, N, offset int64) ([]uint64, []*dbtypes.AddressRow, error) {
	return retrieveAddressTxns(db, address, N, offset,
		internal.SelectAddressLimitNByAddress)
}

func RetriveChartValue(db *sql.DB) (*dbtypes.ChartValue, error) {

	totalcountsql := `SELECT count(*),sum(value) FROM topaddresses;`
	t1countsql := `SELECT count(*),sum(value) FROM public.topaddresses where (value/100000000)>=0 and (value/100000000)<1;;`
	t2countsql := `SELECT count(*),sum(value) FROM public.topaddresses where (value/100000000)>=1 and (value/100000000)<10;`
	t3countsql := `SELECT count(*),sum(value) FROM public.topaddresses where (value/100000000)>=10 and (value/100000000)<100;`
	t4countsql := `SELECT count(*),sum(value) FROM public.topaddresses where (value/100000000)>=100 and (value/100000000)<1000;`
	t5countsql := `SELECT count(*,sum(value)) FROM public.topaddresses where (value/100000000)>=1000 and (value/100000000)<10000;`
	t6countsql := `SELECT count(*),sum(value) FROM public.topaddresses where (value/100000000)>=10000 and (value/100000000)<100000;`
	t7countsql := `SELECT count(*),sum(value) FROM public.topaddresses where (value/100000000)>=100000;`
	var a dbtypes.ChartValue
	totalcount, totalValue := 0, 0.0
	t1count, t1value := 0, 0.0
	t2count, t2value := 0, 0.0
	t3count, t3value := 0, 0.0
	t4count, t4value := 0, 0.0
	t5count, t5value := 0, 0.0
	t6count, t6value := 0, 0.0
	t7count, t7value := 0, 0.0
	err := db.QueryRow(totalcountsql).Scan(&totalcount, &totalValue)
	err = db.QueryRow(t1countsql).Scan(&t1count, &t1value)
	err = db.QueryRow(t2countsql).Scan(&t2count, &t2value)
	err = db.QueryRow(t3countsql).Scan(&t3count, &t3value)
	err = db.QueryRow(t4countsql).Scan(&t4count, &t4value)
	err = db.QueryRow(t5countsql).Scan(&t5count, &t5value)
	err = db.QueryRow(t6countsql).Scan(&t6count, &t6value)
	err = db.QueryRow(t7countsql).Scan(&t7count, &t7value)

	a.BalanceDist = []string{"0 - 1", "1 - 10", "10 - 100", "100 - 1000", "1,000 - 10,000", "10,000 - 100,000", ">100,000"}
	totalValue = totalValue / 100000000
	a.CountList = []int{t1count, t2count, t3count, t4count, t5count, t6count, t7count}
	t1value = t1value / 100000000
	t2value = t2value / 100000000
	t3value = t3value / 100000000
	t4value = t4value / 100000000
	t5value = t5value / 100000000
	t6value = t6value / 100000000
	t7value = t7value / 100000000

	a.SumList = []float64{t1value, t2value, t3value, t4value, t5value, t6value, t7value}
	countp1 := convertTo6(float64(t1count) / float64(totalcount))
	countp2 := convertTo6(float64(t2count) / float64(totalcount))
	countp3 := convertTo6(float64(t3count) / float64(totalcount))
	countp4 := convertTo6(float64(t4count) / float64(totalcount))
	countp5 := convertTo6(float64(t5count) / float64(totalcount))
	countp6 := convertTo6(float64(t6count) / float64(totalcount))
	countp7 := convertTo6(float64(t7count) / float64(totalcount))

	a.CountPercent = []string{fmt.Sprintf("%f", countp1), fmt.Sprintf("%f", countp2), fmt.Sprintf("%f", countp3), fmt.Sprintf("%f", countp4), fmt.Sprintf("%f", countp5), fmt.Sprintf("%f", countp6), fmt.Sprintf("%f", countp7)}

	sump1 := convertTo6(float64(t1value) / float64(totalValue))
	sump2 := convertTo6(float64(t2value) / float64(totalValue))
	sump3 := convertTo6(float64(t3value) / float64(totalValue))
	sump4 := convertTo6(float64(t4value) / float64(totalValue))
	sump5 := convertTo6(float64(t5value) / float64(totalValue))
	sump6 := convertTo6(float64(t6value) / float64(totalValue))
	sump7 := convertTo6(float64(t7value) / float64(totalValue))

	a.SumPercent = []string{fmt.Sprintf("%f", sump1), fmt.Sprintf("%f", sump2), fmt.Sprintf("%f", sump3), fmt.Sprintf("%f", sump4), fmt.Sprintf("%f", sump5), fmt.Sprintf("%f", sump6), fmt.Sprintf("%f", sump7)}
	return &a, err

}

func convertTo6(value float64) float64 {
	value, _ = strconv.ParseFloat(fmt.Sprintf("%.6f", value), 64)
	return value
}

func convertTo3(value float64) float64 {
	value, _ = strconv.ParseFloat(fmt.Sprintf("%.3f", value), 64)
	return value
}

func RetrieveDiffChartData(db *sql.DB) ([]*dbtypes.DiffData, error) {
	rows, err := db.Query(`select min(time), difficulty from blocks group by difficulty order by min(time) asc;`)
	if err != nil {
		return nil, err
	}
	return scanDiffChartQueryRows(rows)
}

func RetrieveTop100Address(db *sql.DB, N, offset int64) ([]uint64, []*dbtypes.TopAddressRow, error) {
	rows, err := db.Query(internal.SelectTop100RichAddress)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	return scanTopAddressQueryRows(rows)
}

func scanDiffChartQueryRows(rows *sql.Rows) (addressRows []*dbtypes.DiffData, err error) {
	for rows.Next() {
		var addr dbtypes.DiffData
		err = rows.Scan(&addr.BlockTime,
			&addr.Difficulty)
		addr.StrTime = time.Unix(int64(addr.BlockTime), 0).Format("2006-01-02 15:04:05")
		//addr.DEndTime = time.Unix(int64(addr.EndTime), 0).Format("2006-01-02 15:04:05")
		addressRows = append(addressRows, &addr)

	}

	return

}

func RetrieveDiffData(db *sql.DB) ([]*dbtypes.DiffData, error) {
	rows, err := db.Query(`select min(height) ,min(time), difficulty from blocks group by difficulty order by min(height) desc;`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	return scanDiffQueryRows(rows)
}

func scanDiffQueryRows(rows *sql.Rows) (addressRows []*dbtypes.DiffData, err error) {
	for rows.Next() {
		var addr dbtypes.DiffData
		err = rows.Scan(&addr.Height, &addr.BlockTime,
			&addr.Difficulty)
		if err != nil {
			return
		}

		/*if vinDbID.Valid {
			addr.VinDbID = uint64(vinDbID.Int64)
		}*/
		addr.StrDiff = fmt.Sprintf("%f", addr.Difficulty)
		addr.StrTime = time.Unix(int64(addr.BlockTime), 0).Format("2006-01-02 15:04:05")
		//addr.DEndTime = time.Unix(int64(addr.EndTime), 0).Format("2006-01-02 15:04:05")
		addressRows = append(addressRows, &addr)
	}

	for i := 0; i < len(addressRows)-1; i++ {
		addr := addressRows[i]
		preDiff := addressRows[i+1].Difficulty

		value := convertTo3((((addr.Difficulty - preDiff) / preDiff) * 100))

		if value > 0 {
			addr.Change = "+" + fmt.Sprintf("%f", value) + "%"
		} else {
			addr.Change = fmt.Sprintf("%f", value) + "%"
		}
	}
	return
}

func scanTopAddressQueryRows(rows *sql.Rows) (ids []uint64, addressRows []*dbtypes.TopAddressRow, err error) {
	for rows.Next() {
		var id uint64
		var addr dbtypes.TopAddressRow
		err = rows.Scan(&id, &addr.Address, &addr.Value,
			&addr.TxCount, &addr.StartTime, &addr.EndTime)
		if err != nil {
			return
		}

		/*if vinDbID.Valid {
			addr.VinDbID = uint64(vinDbID.Int64)
		}*/

		ids = append(ids, id)
		addr.StringVal = fmt.Sprintf("%f", float64(addr.Value)/100000000)
		addr.DStartTime = time.Unix(int64(addr.StartTime), 0).Format("2006-01-02 15:04:05")
		addr.DEndTime = time.Unix(int64(addr.EndTime), 0).Format("2006-01-02 15:04:05")
		addressRows = append(addressRows, &addr)
	}
	return
}
func RetrieveBlocksizejson(db *sql.DB, N, offset int64) ([]uint64, *dbtypes.BlocksizeJson, error) {
	//now := time.Now()
	//before90day := strconv.FormatInt(24*day,10)
	//d, _ := time.ParseDuration("-"+before90day+"h")  //24h*90
	//d1 := now.Add(d)

	rowsAll, err := db.Query("select sum(size) as totalsize,ROUND(avg(size),0) as avgsize,sum(numtx) as txsum,to_char(to_timestamp(time),'yyyy-MM-dd') as date  from blocks  group by date ORDER BY date")
	if err != nil {
		return nil, nil, nil
	}
	log.Info(rowsAll)
	defer func() {
		if e := rowsAll.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()
	return scanBlocksizeRows(rowsAll)
}
func scanBlocksizeRows(rows *sql.Rows) (ids []uint64, blocksizejson *dbtypes.BlocksizeJson, err error) {
	blocksizejsons := &dbtypes.BlocksizeJson{make([]int64, 0), make([]int64, 0), make([]int64, 0), make([]string, 0)}
	for rows.Next() {
		var blocksize dbtypes.Blocksize

		err1 := rows.Scan(&blocksize.TotalSize, &blocksize.AvgSize, &blocksize.TotalTx, &blocksize.Date)
		if err1 != nil {
			fmt.Println(err1)
			return
		}
		log.Info(blocksize)
		blocksizejsons.TotalSize = append(blocksizejsons.TotalSize, blocksize.TotalSize)
		blocksizejsons.AvgSize = append(blocksizejsons.AvgSize, blocksize.AvgSize)
		blocksizejsons.TotalTx = append(blocksizejsons.TotalTx, blocksize.TotalTx)
		blocksizejsons.Date = append(blocksizejsons.Date, blocksize.Date)
		log.Info(blocksizejson)
	}
	blocksizejson = blocksizejsons
	return
}

func RetrieveBlockVerJson(db *sql.DB) (*dbtypes.BlockVerJson, error) {
	rowsAll, err := db.Query("select to_char(to_timestamp(time),'YYYYMMDD') as date,count(*) FILTER (WHERE version = 0) AS v0,count(*) FILTER (WHERE version = 1) as v1,count(*) FILTER (WHERE version <> 1 and  version <> 0) as other from blocks  group by date order by date")
	if err != nil {
		log.Error(err)
		return nil, err
	}
	defer func() {
		if e := rowsAll.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	return scanBlockVersionJson(rowsAll)
}
func scanBlockVersionJson(rows *sql.Rows) (*dbtypes.BlockVerJson, error) {
	blockVerJson := &dbtypes.BlockVerJson{make([]string, 0), make([]float64, 0), make([]float64, 0), make([]float64, 0)}
	for rows.Next() {
		var str string
		var v0 int64
		var v1 int64
		var other int64
		err := rows.Scan(&str, &v0, &v1, &other)
		if err != nil {
			log.Error(err)
			return nil, err
		}
		temppercentv0 := float64(v0) / float64((v0 + v1 + other)) * 100
		temppercentv1 := float64(v1) / float64((v0 + v1 + other)) * 100
		temppercentother := float64(other) / float64((v0 + v1 + other)) * 100

		percentv0, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", temppercentv0), 2)
		percentv1, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", temppercentv1), 2)
		percentother, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", temppercentother), 2)

		blockVerJson.Date = append(blockVerJson.Date, str)
		blockVerJson.V0 = append(blockVerJson.V0, percentv0)
		blockVerJson.V1 = append(blockVerJson.V1, percentv1)
		blockVerJson.Other = append(blockVerJson.Other, percentother)
	}
	return blockVerJson, nil
}

func RetrieveScriptTypejson(db *sql.DB, N, offset int64) (scriptTypejson *dbtypes.ScriptTypejson, err error) {
	//now := time.Now()
	//before90day := strconv.FormatInt(24*day,10)
	//d, _ := time.ParseDuration("-"+before90day+"h")  //24h*90
	//d1 := now.Add(d)
	scriptTypejsons := &dbtypes.ScriptTypejson{make([]string, 0), make([]dbtypes.Value_type, 0), make([]dbtypes.Value_type, 0), make([]dbtypes.Value_type, 0), make([]dbtypes.Value_type, 0), make(map[string]*dbtypes.ScriptInfo)}
	rowsType, err := db.Query("select script_type from vouts group by script_type")
	if err != nil {
		return nil, nil
	}
	for rowsType.Next() {
		var scriptType string
		err1 := rowsType.Scan(&scriptType)
		if err1 != nil {
			log.Error(err1)
			return nil, err1
		}
		scriptTypejsons.Type = append(scriptTypejsons.Type, scriptType)
		scriptTypejsons.ScriptInfo[scriptType] = &dbtypes.ScriptInfo{}
	}
	// temp data
	var infoType dbtypes.Value_type
	// Amount_type_vouts
	rows_amount_type_vouts, err := db.Query("select count(*),script_type from vouts group by script_type")
	if err != nil {
		log.Error(err)
		return nil, err
	}
	for rows_amount_type_vouts.Next() {
		err2 := rows_amount_type_vouts.Scan(&infoType.Value, &infoType.Script)
		if err2 != nil {
			log.Error(err2)
			return nil, err2
		}
		scriptTypejsons.Amount_type_vouts = append(scriptTypejsons.Amount_type_vouts, infoType)
		scriptTypejsons.ScriptInfo[infoType.Script].AmountVouts = infoType.Value
	}
	// Num_type_vouts
	rows_sum_type_vouts, err3 := db.Query("select sum(value),script_type from vouts group by script_type")
	if err3 != nil {
		log.Error(err3)
		return nil, err3
	}
	for rows_sum_type_vouts.Next() {
		err2 := rows_sum_type_vouts.Scan(&infoType.Value, &infoType.Script)
		if err2 != nil {
			log.Error(err2)
			return nil, err2
		}
		scriptTypejsons.Num_type_vouts = append(scriptTypejsons.Num_type_vouts, infoType)
		scriptTypejsons.ScriptInfo[infoType.Script].SumVouts = infoType.Value
	}
	// Amount_type_vins / num_type_vins
	rows_type_vins, err4 := db.Query("select * from scriptinfo_vins")
	if err4 != nil {
		log.Error(err4)
		return nil, err4
	}
	for rows_type_vins.Next() {
		var accountValue int64
		var sumValue int64
		var script_type string
		rows_type_vins.Scan(&accountValue, &sumValue, &script_type)
		infoType = dbtypes.Value_type{accountValue, script_type}
		scriptTypejsons.Amount_type_vins = append(scriptTypejsons.Amount_type_vins, infoType)
		infoType = dbtypes.Value_type{sumValue, script_type}
		scriptTypejsons.Sum_type_vins = append(scriptTypejsons.Sum_type_vins, infoType)

		scriptTypejsons.ScriptInfo[script_type].AmountVins = accountValue
		scriptTypejsons.ScriptInfo[script_type].SumVins = sumValue

	}
	scriptTypejson = scriptTypejsons
	return

}

// func scanScriptType(rows *sql.Rows) (ids []uint64, blocksizejson *dbtypes.BlocksizeJson, err error) {
// 	blocksizejsons := &dbtypes.BlocksizeJson{make([]int64, 0), make([]int64, 0), make([]int64, 0), make([]string, 0)}
// 	for rows.Next() {
// 		var blocksize dbtypes.Blocksize

// 		err1 := rows.Scan(&blocksize.TotalSize, &blocksize.AvgSize, &blocksize.TotalTx, &blocksize.Date)
// 		if err1 != nil {
// 			fmt.Println(err1)
// 			return
// 		}
// 		log.Info(blocksize)
// 		blocksizejsons.TotalSize = append(blocksizejsons.TotalSize, blocksize.TotalSize)
// 		blocksizejsons.AvgSize = append(blocksizejsons.AvgSize, blocksize.AvgSize)
// 		blocksizejsons.TotalTx = append(blocksizejsons.TotalTx, blocksize.TotalTx)
// 		blocksizejsons.Date = append(blocksizejsons.Date, blocksize.Date)
// 		log.Info(blocksizejson)
// 	}
// 	blocksizejson = blocksizejsons
// 	return
// }

func retrieveAddressTxns(db *sql.DB, address string, N, offset int64,
	statement string) ([]uint64, []*dbtypes.AddressRow, error) {
	rows, err := db.Query(statement, address, N, offset)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	return scanAddressQueryRows(rows)
}

func scanAddressQueryRows(rows *sql.Rows) (ids []uint64, addressRows []*dbtypes.AddressRow, err error) {
	for rows.Next() {
		var id uint64
		var addr dbtypes.AddressRow
		var spendingTxHash sql.NullString
		var spendingTxDbID, spendingTxVinIndex, vinDbID sql.NullInt64
		err = rows.Scan(&id, &addr.Address, &addr.FundingTxDbID, &addr.FundingTxHash,
			&addr.FundingTxVoutIndex, &addr.VoutDbID, &addr.Value,
			&spendingTxDbID, &spendingTxHash, &spendingTxVinIndex, &vinDbID)
		if err != nil {
			return
		}

		if spendingTxDbID.Valid {
			addr.SpendingTxDbID = uint64(spendingTxDbID.Int64)
		}
		if spendingTxHash.Valid {
			addr.SpendingTxHash = spendingTxHash.String
		}
		if spendingTxVinIndex.Valid {
			addr.SpendingTxVinIndex = uint32(spendingTxVinIndex.Int64)
		}
		if vinDbID.Valid {
			addr.VinDbID = uint64(vinDbID.Int64)
		}

		ids = append(ids, id)
		addressRows = append(addressRows, &addr)
	}
	return
}

func RetrieveAddressIDsByOutpoint(db *sql.DB, txHash string,
	voutIndex uint32) ([]uint64, []string, error) {
	var ids []uint64
	var addresses []string
	rows, err := db.Query(internal.SelectAddressIDsByFundingOutpoint, txHash, voutIndex)
	if err != nil {
		return ids, addresses, err
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	for rows.Next() {
		var id uint64
		var addr string
		err = rows.Scan(&id, &addr)
		if err != nil {
			break
		}

		ids = append(ids, id)
		addresses = append(addresses, addr)
	}

	return ids, addresses, err
}

func RetrieveAllVinDbIDs(db *sql.DB) (vinDbIDs []uint64, err error) {
	rows, err := db.Query(internal.SelectVinIDsALL)
	if err != nil {
		return
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	for rows.Next() {
		var id uint64
		err = rows.Scan(&id)
		if err != nil {
			break
		}

		vinDbIDs = append(vinDbIDs, id)
	}

	return
}

func RetrieveSpendingTxByVinID(db *sql.DB, vinDbID uint64) (tx string,
	voutIndex uint32, tree int8, err error) {
	err = db.QueryRow(internal.SelectSpendingTxByVinID, vinDbID).Scan(&tx, &voutIndex, &tree)
	return
}

func RetrieveFundingOutpointByTxIn(db *sql.DB, txHash string,
	vinIndex uint32) (id uint64, tx string, index uint32, tree int8, err error) {
	err = db.QueryRow(internal.SelectFundingOutpointByTxIn, txHash, vinIndex).
		Scan(&id, &tx, &index, &tree)
	return
}

func RetrieveFundingOutpointByVinID(db *sql.DB, vinDbID uint64) (tx string, index uint32, tree int8, err error) {
	err = db.QueryRow(internal.SelectFundingOutpointByVinID, vinDbID).
		Scan(&tx, &index, &tree)
	return
}

func RetrieveVinByID(db *sql.DB, vinDbID uint64) (prevOutHash string, prevOutVoutInd uint32,
	prevOutTree int8, txHash string, txVinInd uint32, txTree int8, err error) {
	var id uint64
	err = db.QueryRow(internal.SelectAllVinInfoByID, vinDbID).
		Scan(&id, &txHash, &txVinInd, &txTree,
			&prevOutHash, &prevOutVoutInd, &prevOutTree)
	return
}

func RetrieveFundingTxByTxIn(db *sql.DB, txHash string, vinIndex uint32) (id uint64, tx string, err error) {
	err = db.QueryRow(internal.SelectFundingTxByTxIn, txHash, vinIndex).
		Scan(&id, &tx)
	return
}

func RetrieveFundingTxsByTx(db *sql.DB, txHash string) ([]uint64, []*dbtypes.Tx, error) {
	var ids []uint64
	var txs []*dbtypes.Tx
	rows, err := db.Query(internal.SelectFundingTxsByTx, txHash)
	if err != nil {
		return ids, txs, err
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	for rows.Next() {
		var id uint64
		var tx dbtypes.Tx
		err = rows.Scan(&id, &tx)
		if err != nil {
			break
		}

		ids = append(ids, id)
		txs = append(txs, &tx)
	}

	return ids, txs, err
}

func RetrieveSpendingTxByTxOut(db *sql.DB, txHash string,
	voutIndex uint32) (id uint64, tx string, vin uint32, tree int8, err error) {
	err = db.QueryRow(internal.SelectSpendingTxByPrevOut,
		txHash, voutIndex).Scan(&id, &tx, &vin, &tree)
	return
}

func RetrieveSpendingTxsByFundingTx(db *sql.DB, fundingTxID string) (dbIDs []uint64,
	txns []string, vinInds []uint32, voutInds []uint32, err error) {
	rows, err := db.Query(internal.SelectSpendingTxsByPrevTx, fundingTxID)
	if err != nil {
		return
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	for rows.Next() {
		var id uint64
		var tx string
		var vin, vout uint32
		err = rows.Scan(&id, &tx, &vin, &vout)
		if err != nil {
			break
		}

		dbIDs = append(dbIDs, id)
		txns = append(txns, tx)
		vinInds = append(vinInds, vin)
		voutInds = append(voutInds, vout)
	}

	return
}

func RetrieveDbTxByHash(db *sql.DB, txHash string) (id uint64, dbTx *dbtypes.Tx, err error) {
	dbTx = new(dbtypes.Tx)
	vinDbIDs := dbtypes.UInt64Array(dbTx.VinDbIds)
	voutDbIDs := dbtypes.UInt64Array(dbTx.VoutDbIds)
	err = db.QueryRow(internal.SelectFullTxByHash, txHash).Scan(&id,
		&dbTx.BlockHash, &dbTx.BlockHeight, &dbTx.BlockTime, &dbTx.Time,
		&dbTx.TxType, &dbTx.Version, &dbTx.Tree, &dbTx.TxID, &dbTx.BlockIndex,
		&dbTx.Locktime, &dbTx.Expiry, &dbTx.Size, &dbTx.Spent, &dbTx.Sent,
		&dbTx.Fees, &dbTx.NumVin, &vinDbIDs, &dbTx.NumVout, &voutDbIDs)
	return
}

func RetrieveFullTxByHash(db *sql.DB, txHash string) (id uint64,
	blockHash string, blockHeight int64, blockTime int64, time int64,
	txType int16, version int32, tree int8, blockInd uint32,
	lockTime, expiry int32, size uint32, spent, sent, fees int64,
	numVin int32, vinDbIDs []int64, numVout int32, voutDbIDs []int64,
	err error) {
	var hash string
	err = db.QueryRow(internal.SelectFullTxByHash, txHash).Scan(&id, &blockHash,
		&blockHeight, &blockTime, &time, &txType, &version, &tree,
		&hash, &blockInd, &lockTime, &expiry, &size, &spent, &sent, &fees,
		&numVin, &vinDbIDs, &numVout, &voutDbIDs)
	return
}

func RetrieveTxByHash(db *sql.DB, txHash string) (id uint64, blockHash string, blockInd uint32, tree int8, err error) {
	err = db.QueryRow(internal.SelectTxByHash, txHash).Scan(&id, &blockHash, &blockInd, &tree)
	return
}

func RetrieveRegularTxByHash(db *sql.DB, txHash string) (id uint64, blockHash string, blockInd uint32, err error) {
	err = db.QueryRow(internal.SelectRegularTxByHash, txHash).Scan(&id, &blockHash, &blockInd)
	return
}

func RetrieveStakeTxByHash(db *sql.DB, txHash string) (id uint64, blockHash string, blockInd uint32, err error) {
	err = db.QueryRow(internal.SelectStakeTxByHash, txHash).Scan(&id, &blockHash, &blockInd)
	return
}

func RetrieveTxsByBlockHash(db *sql.DB, blockHash string) (ids []uint64, txs []string, blockInds []uint32, trees []int8, err error) {
	rows, err := db.Query(internal.SelectTxsByBlockHash, blockHash)
	if err != nil {
		return
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	for rows.Next() {
		var id uint64
		var tx string
		var bind uint32
		var tree int8
		err = rows.Scan(&id, &tx, &bind, &tree)
		if err != nil {
			break
		}

		ids = append(ids, id)
		txs = append(txs, tx)
		blockInds = append(blockInds, bind)
		trees = append(trees, tree)
	}

	return
}

func InsertBlock(db *sql.DB, dbBlock *dbtypes.Block, isValid, checked bool) (uint64, error) {
	insertStatement := internal.MakeBlockInsertStatement(dbBlock, checked)
	var id uint64
	err := db.QueryRow(insertStatement,
		dbBlock.Hash, dbBlock.Height, dbBlock.Size, isValid, dbBlock.Version,
		dbBlock.MerkleRoot, dbBlock.StakeRoot,
		dbBlock.NumTx, dbBlock.NumRegTx, dbBlock.NumStakeTx,
		dbBlock.Time, dbBlock.Nonce, dbBlock.VoteBits,
		dbBlock.FinalState, dbBlock.Voters, dbBlock.FreshStake,
		dbBlock.Revocations, dbBlock.PoolSize, dbBlock.Bits,
		dbBlock.SBits, dbBlock.Difficulty, dbBlock.ExtraData,
		dbBlock.StakeVersion, dbBlock.PreviousHash).Scan(&id)
	return id, err
}

func UpdateLastBlock(db *sql.DB, blockDbID uint64, isValid bool) error {
	res, err := db.Exec(internal.UpdateLastBlockValid, blockDbID, isValid)
	if err != nil {
		return err
	}
	numRows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if numRows != 1 {
		return fmt.Errorf("UpdateLastBlock failed to update exactly 1 row (%d)", numRows)
	}
	return nil
}

func RetrieveBestBlockHeight(db *sql.DB) (height uint64, hash string, id uint64, err error) {
	err = db.QueryRow(internal.RetrieveBestBlockHeight).Scan(&id, &hash, &height)
	return
}

func RetrieveVoutValue(db *sql.DB, txHash string, voutIndex uint32) (value uint64, err error) {
	err = db.QueryRow(internal.RetrieveVoutValue, txHash, voutIndex).Scan(&value)
	return
}

func RetrieveVoutValues(db *sql.DB, txHash string) (values []uint64, txInds []uint32, txTrees []int8, err error) {
	var rows *sql.Rows
	rows, err = db.Query(internal.RetrieveVoutValues, txHash)
	if err != nil {
		return
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	for rows.Next() {
		var v uint64
		var ind uint32
		var tree int8
		err = rows.Scan(&v, &ind, &tree)
		if err != nil {
			break
		}

		values = append(values, v)
		txInds = append(txInds, ind)
		txTrees = append(txTrees, tree)
	}

	return
}

func InsertBlockPrevNext(db *sql.DB, blockDbID uint64,
	hash, prev, next string) error {
	rows, err := db.Query(internal.InsertBlockPrevNext, blockDbID, prev, hash, next)
	if err == nil {
		return rows.Close()
	}
	return err
}

func UpdateBlockNext(db *sql.DB, blockDbID uint64, next string) error {
	res, err := db.Exec(internal.UpdateBlockNext, blockDbID, next)
	if err != nil {
		return err
	}
	numRows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if numRows != 1 {
		return fmt.Errorf("UpdateBlockNext failed to update exactly 1 row (%d)", numRows)
	}
	return nil
}

func InsertVin(db *sql.DB, dbVin dbtypes.VinTxProperty) (id uint64, err error) {
	err = db.QueryRow(internal.InsertVinRow,
		dbVin.TxID, dbVin.TxIndex, dbVin.TxTree,
		dbVin.PrevTxHash, dbVin.PrevTxIndex, dbVin.PrevTxTree).Scan(&id)
	return
}

func InsertVins(db *sql.DB, dbVins dbtypes.VinTxPropertyARRAY) ([]uint64, error) {
	dbtx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("unable to begin database transaction: %v", err)
	}

	stmt, err := dbtx.Prepare(internal.InsertVinRow)
	if err != nil {
		log.Errorf("Vin INSERT prepare: %v", err)
		_ = dbtx.Rollback() // try, but we want the Prepare error back
		return nil, err
	}

	// TODO/Question: Should we skip inserting coinbase txns, which have same PrevTxHash?

	ids := make([]uint64, 0, len(dbVins))
	for _, vin := range dbVins {
		var id uint64
		err = stmt.QueryRow(vin.TxID, vin.TxIndex, vin.TxTree,
			vin.PrevTxHash, vin.PrevTxIndex, vin.PrevTxTree).Scan(&id)
		if err != nil {
			_ = stmt.Close() // try, but we want the QueryRow error back
			if errRoll := dbtx.Rollback(); errRoll != nil {
				log.Errorf("Rollback failed: %v", errRoll)
			}
			return ids, fmt.Errorf("InsertVins INSERT exec failed: %v", err)
		}
		ids = append(ids, id)
	}

	// Close prepared statement. Ignore errors as we'll Commit regardless.
	_ = stmt.Close()

	return ids, dbtx.Commit()
}

func InsertVout(db *sql.DB, dbVout *dbtypes.Vout, checked bool) (uint64, error) {
	insertStatement := internal.MakeVoutInsertStatement(checked)
	var id uint64
	err := db.QueryRow(insertStatement,
		dbVout.TxHash, dbVout.TxIndex, dbVout.TxTree,
		dbVout.Value, dbVout.Version,
		dbVout.ScriptPubKey, dbVout.ScriptPubKeyData.ReqSigs,
		dbVout.ScriptPubKeyData.Type,
		pq.Array(dbVout.ScriptPubKeyData.Addresses)).Scan(&id)
	return id, err
}

func InsertVouts(db *sql.DB, dbVouts []*dbtypes.Vout, checked bool) ([]uint64, []dbtypes.AddressRow, error) {
	addressRows := make([]dbtypes.AddressRow, 0, len(dbVouts)*2)
	dbtx, err := db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("unable to begin database transaction: %v", err)
	}

	stmt, err := dbtx.Prepare(internal.MakeVoutInsertStatement(checked))
	if err != nil {
		log.Errorf("Vout INSERT prepare: %v", err)
		_ = dbtx.Rollback() // try, but we want the Prepare error back
		return nil, nil, err
	}

	ids := make([]uint64, 0, len(dbVouts))
	for _, vout := range dbVouts {
		var id uint64
		err := stmt.QueryRow(
			vout.TxHash, vout.TxIndex, vout.TxTree, vout.Value, vout.Version,
			vout.ScriptPubKey, vout.ScriptPubKeyData.ReqSigs,
			vout.ScriptPubKeyData.Type,
			pq.Array(vout.ScriptPubKeyData.Addresses)).Scan(&id)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			_ = stmt.Close() // try, but we want the QueryRow error back
			if errRoll := dbtx.Rollback(); errRoll != nil {
				log.Errorf("Rollback failed: %v", errRoll)
			}
			return nil, nil, err
		}
		for _, addr := range vout.ScriptPubKeyData.Addresses {
			addressRows = append(addressRows, dbtypes.AddressRow{
				Address:            addr,
				FundingTxHash:      vout.TxHash,
				FundingTxVoutIndex: vout.TxIndex,
				VoutDbID:           id,
				Value:              vout.Value,
			})
		}
		ids = append(ids, id)
	}

	// Close prepared statement. Ignore errors as we'll Commit regardless.
	_ = stmt.Close()

	return ids, addressRows, dbtx.Commit()
}

func InsertAddressOut(db *sql.DB, dbA *dbtypes.AddressRow) (uint64, error) {
	var id uint64
	err := db.QueryRow(internal.InsertAddressRow, dbA.Address,
		dbA.FundingTxDbID, dbA.FundingTxHash, dbA.FundingTxVoutIndex,
		dbA.VoutDbID, dbA.Value).Scan(&id)
	return id, err
}

func InsertAddressOuts(db *sql.DB, dbAs []*dbtypes.AddressRow) ([]uint64, error) {
	// Create the address table if it does not exist
	tableName := "addresses"
	if haveTable, _ := TableExists(db, tableName); !haveTable {
		if err := CreateTable(db, tableName); err != nil {
			log.Errorf("Failed to create table %s: %v", tableName, err)
		}
	}

	dbtx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("unable to begin database transaction: %v", err)
	}

	stmt, err := dbtx.Prepare(internal.InsertAddressRow)
	if err != nil {
		log.Errorf("AddressRow INSERT prepare: %v", err)
		_ = dbtx.Rollback() // try, but we want the Prepare error back
		return nil, err
	}

	ids := make([]uint64, 0, len(dbAs))
	for _, dbA := range dbAs {
		var id uint64
		err := stmt.QueryRow(dbA.Address, dbA.FundingTxDbID, dbA.FundingTxHash,
			dbA.FundingTxVoutIndex, dbA.VoutDbID, dbA.Value).Scan(&id)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			_ = stmt.Close() // try, but we want the QueryRow error back
			if errRoll := dbtx.Rollback(); errRoll != nil {
				log.Errorf("Rollback failed: %v", errRoll)
			}
			return nil, err
		}
		ids = append(ids, id)
	}

	// Close prepared statement. Ignore errors as we'll Commit regardless.
	_ = stmt.Close()

	return ids, dbtx.Commit()
}

func InsertTx(db *sql.DB, dbTx *dbtypes.Tx, checked bool) (uint64, error) {
	insertStatement := internal.MakeTxInsertStatement(checked)
	var id uint64
	err := db.QueryRow(insertStatement,
		dbTx.BlockHash, dbTx.BlockHeight, dbTx.BlockTime, dbTx.Time,
		dbTx.TxType, dbTx.Version, dbTx.Tree, dbTx.TxID, dbTx.BlockIndex,
		dbTx.Locktime, dbTx.Expiry, dbTx.Size, dbTx.Spent, dbTx.Sent, dbTx.Fees,
		dbTx.NumVin, dbTx.Vins, dbtypes.UInt64Array(dbTx.VinDbIds),
		dbTx.NumVout, pq.Array(dbTx.Vouts), dbtypes.UInt64Array(dbTx.VoutDbIds)).Scan(&id)
	return id, err
}

func InsertTxns(db *sql.DB, dbTxns []*dbtypes.Tx, checked bool) ([]uint64, error) {
	dbtx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("unable to begin database transaction: %v", err)
	}

	stmt, err := dbtx.Prepare(internal.MakeTxInsertStatement(checked))
	if err != nil {
		log.Errorf("Vout INSERT prepare: %v", err)
		_ = dbtx.Rollback() // try, but we want the Prepare error back
		return nil, err
	}

	ids := make([]uint64, 0, len(dbTxns))
	for _, tx := range dbTxns {
		var id uint64
		err := stmt.QueryRow(
			tx.BlockHash, tx.BlockHeight, tx.BlockTime, tx.Time,
			tx.TxType, tx.Version, tx.Tree, tx.TxID, tx.BlockIndex,
			tx.Locktime, tx.Expiry, tx.Size, tx.Spent, tx.Sent, tx.Fees,
			tx.NumVin, tx.Vins, dbtypes.UInt64Array(tx.VinDbIds),
			tx.NumVout, pq.Array(tx.Vouts), dbtypes.UInt64Array(tx.VoutDbIds)).Scan(&id)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			_ = stmt.Close() // try, but we want the QueryRow error back
			if errRoll := dbtx.Rollback(); errRoll != nil {
				log.Errorf("Rollback failed: %v", errRoll)
			}
			return nil, err
		}
		ids = append(ids, id)
	}

	// Close prepared statement. Ignore errors as we'll Commit regardless.
	_ = stmt.Close()

	return ids, dbtx.Commit()
}

// update fees stat
func RetrieveFeesStatLastDay(db *sql.DB) (d int64, isInit bool, err error) {
	err = db.QueryRow(`SELECT MAX(time) d FROM fees_stat;`).Scan(&d)
	if err != nil {
		err = db.QueryRow(`SELECT MIN(time) d FROM blocks;`).Scan(&d)
		isInit = true
		return
	}
	return
}

func UpdateFeesStatOneDay(db *sql.DB, day time.Time) (err error) {
	startDate := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	endDate := startDate.AddDate(0, 0, 1)
	size, min, max := 0, 0, 0
	err = db.QueryRow(internal.RetrieveRangeBlockHeightOneDay, startDate.Unix(), endDate.Unix()).Scan(&size, &min, &max)
	if err != nil {
		return err
	}
	if min < 2 {
		min = 2
	}
	rewards, fees := 0, 0
	err = db.QueryRow(internal.RetrieveRewardsFeesOneDay, min, max).Scan(&rewards, &fees)
	if err != nil {
		return err
	}
	feesRewards := float64(fees) / float64(rewards)
	feesPerkb := hcutil.Amount(fees).ToCoin() / float64(size) * 1000
	id := 0
	err = db.QueryRow(internal.InsertFeesStat, startDate.Unix(), fees, rewards, size, feesRewards, feesPerkb).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	err = nil
	return
}

// get fees stat
func RetrieveFeesStat(db *sql.DB) (res []*dbtypes.FeesStat) {
	rows, err := db.Query(internal.RetrieveLast90FeesStat)
	if err != nil {
		log.Error(err)
		return
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()
	for rows.Next() {
		var timestamp int64
		var fees int64
		var feesRewards float64
		var feesPerkb float64
		err = rows.Scan(&timestamp, &fees, &feesRewards, &feesPerkb)
		if err != nil {
			log.Error(err)
			continue
		}
		stat := dbtypes.FeesStat{
			Time:        time.Unix(timestamp, 0).Format("2006/01/02"),
			Fees:        hcutil.Amount(fees).ToCoin(),
			FeesRewards: feesRewards * 100,
			FeesPerkb:   feesPerkb}
		res = append(res, &stat)
	}
	return
}

// update mempool history
func UpdateMempoolHistory(db *sql.DB, t time.Time, size, bytes int64) (id int, err error) {
	pressT := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	err = db.QueryRow(internal.InsertMempoolHistory, pressT.Unix(), size, bytes).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return
	}
	return id, nil
}

// get mempool history
func RetrieveMempoolRecentHistory(db *sql.DB) (res []*dbtypes.MempoolHistory) {
	rows, err := db.Query(internal.RetrieveLastTwoDaysMempoolHistory, time.Now().AddDate(0, 0, -2).Unix())
	if err != nil {
		log.Error(err)
		return
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()
	local, _ := time.LoadLocation("Asia/Shanghai")
	for rows.Next() {
		var timestamp int64
		var size int64
		var bs int64
		err = rows.Scan(&timestamp, &size, &bs)
		if err != nil {
			log.Error(err)
			continue
		}
		mh := dbtypes.MempoolHistory{
			Time:        time.Unix(timestamp, 0).In(local).Format("2006-01-02 15:04"),
			Size:        size,
			Bytes:       bs}
		res = append(res, &mh)
	}
	return
}

// get mempool history kline
func RetrieveMempoolHistoryKline(db *sql.DB) (res []*dbtypes.MempoolHistory) {
	rows, err := db.Query(internal.RetrieveMempoolHistoryKline)
	if err != nil {
		log.Error(err)
		return
	}
	defer func() {
		if e := rows.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()
	for rows.Next() {
		var timestamp int64
		var o int64
		var c int64
		var h int64
		var l int64
		err = rows.Scan(&timestamp, &o, &c, &h, &l)
		if err != nil {
			log.Error(err)
			continue
		}
		mh := dbtypes.MempoolHistory{
			Time:        time.Unix(timestamp, 0).Format("2006-01-02"),
			Open:        o,
			Close:       c,
			High:        h,
			Low:         l}
		res = append(res, &mh)
	}
	return
}

// update mempool history kline
func RetrieveMempoolHistoryKlineLastDay(db *sql.DB) (d int64, isInit bool, err error) {
	err = db.QueryRow(`SELECT MAX(time) d FROM mempool_history WHERE is_day = true;`).Scan(&d)
	if err != nil {
		err = db.QueryRow(`SELECT MIN(time) d FROM mempool_history;`).Scan(&d)
		isInit = true
		return
	}
	return
}

func UpdateMempoolHistoryKlineOneDay(db *sql.DB, day time.Time) (err error) {
	startDate := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	endDate := startDate.AddDate(0, 0, 1)
	h, l, o, c := 0, 0, 0, 0
	err = db.QueryRow(internal.RetrieveMempoolHistoryMinMaxOneDay, startDate.Unix(), endDate.Unix()).Scan(&h, &l)
	if err != nil {
		return err
	}
	err = db.QueryRow(internal.RetrieveMempoolHistoryOpenOneDay, startDate.Unix(), endDate.Unix()).Scan(&o)
	if err != nil {
		return err
	}
	err = db.QueryRow(internal.RetrieveMempoolHistoryCloseOneDay, startDate.Unix(), endDate.Unix()).Scan(&c)
	if err != nil {
		return err
	}
	count := 0
	err = db.QueryRow(internal.RetrieveMempoolHistoryTimeExist, startDate.Unix()).Scan(&count)
	if err != nil {
		return err
	}
	id := 0
	if count >= 1 {
		err = db.QueryRow(internal.UpdateMempoolHistoryKline, o, c, h, l, startDate.Unix()).Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
	} else {
		err = db.QueryRow(internal.InsertMempoolHistoryKline, o, c, h, l, startDate.Unix()).Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	err = nil
	return
}

func updateScriptInfo(db *sql.DB, first bool) error {
	log.Info("updateScriptInfo")
	if first {
		db.Query("TRUNCATE TABLE scriptinfo_vins")
	}

	// create table
	db.Query("create table IF not EXISTS public.scriptinfo_vins(count_value decimal,sum_value decimal,script_type varchar(50))")
	// query count every script type
	rowsCount, err := db.Query("select count(*),vouts.script_type from vins,vouts where vins.prev_tx_hash = vouts.tx_hash group by vouts.script_type")
	if err != nil {
		log.Error(err)
		return err
	}

	// query totalvalue every script type
	rowsSum, err := db.Query("select sum(vouts.value),vouts.script_type from vins,vouts where vins.prev_tx_hash = vouts.tx_hash group by vouts.script_type")
	if err != nil {
		log.Error(err)
		return err
	}
	defer func() {
		if e := rowsCount.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
		if e := rowsSum.Close(); e != nil {
			log.Errorf("Close of Query failed: %v", e)
		}
	}()

	for rowsCount.Next() {
		var value int64
		var script_type string
		err = rowsCount.Scan(&value, &script_type)
		if err != nil {
			log.Error(err)
			return err
		}
		if first {
			_, err = db.Query(`insert into scriptinfo_vins(count_value,script_type)values($1,$2)`, value, script_type)
		} else {
			_, err = db.Exec(`UPDATE scriptinfo_vins SET count_value = $1 WHERE script_type = $2`, value, script_type)
		}

		if err != nil {
			log.Error(err)
			return err
		}
	}
	for rowsSum.Next() {
		var value int64
		var script_type string
		err = rowsSum.Scan(&value, &script_type)
		if err != nil {
			log.Error(err)
			return err
		}

		_, err = db.Exec(`UPDATE scriptinfo_vins SET sum_value = $1 WHERE script_type = $2 `, value, script_type)
		if err != nil {
			log.Error(err)
			return err
		}
	}
	return nil
}
