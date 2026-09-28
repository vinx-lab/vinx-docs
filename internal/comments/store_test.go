package comments

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	tu "github.com/vinx-lab/vinx-docs/internal/testutil"
)

// 页面一打开就同时请求 /api/comments 和 /api/agents，每个请求各自打开一次批注库。
// 新家目录里库文件还不存在，几个连接会同时建库、切 WAL、建表；这些步骤不能因为别的连接
// 正在做同样的事而失败（修复前每 60 轮通常至少出现一次 "database is locked"，接口回 500）。
func TestConcurrentFirstConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("要反复建库，约 10 秒")
	}
	base := tu.TempDir(t)
	const rounds, workers = 60, 6
	for round := 0; round < rounds; round++ {
		db := filepath.Join(base, fmt.Sprint(round), "vinx.db")
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				conn, err := Connect(db)
				if err != nil {
					errs <- fmt.Errorf("Connect: %w", err)
					return
				}
				defer conn.Close()
				if i%2 == 0 {
					_, err = conn.Online()
				} else {
					_, err = conn.Listing("", nil, "")
				}
				if err == nil {
					_, err = conn.Counts()
				}
				if err != nil {
					errs <- fmt.Errorf("查询: %w", err)
				}
			}(i)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("第 %d 轮并发首次打开批注库失败：%v", round, err)
		}
	}
}
