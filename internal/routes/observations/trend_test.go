package observations
import("context";"fmt";"net/http";"net/http/httptest";"testing";"time";"github.com/stretchr/testify/require")
func TestTrendBoundsAndFiltering(t *testing.T){
 now:=time.Unix(1792570000,0);end:=now.Unix()/21600*21600
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  require.Equal(t,"/api/v1/query_range",r.URL.Path)
  require.Equal(t,"21600",r.URL.Query().Get("step"))
  require.Equal(t,fmt.Sprint(end-30*86400),r.URL.Query().Get("start"))
  require.Contains(t,r.URL.Query().Get("query"),`workspace="team"`)
  fmt.Fprintf(w,`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"node":"n","accelerator_uuid":"g","product":"T4","secret":"hidden"},"values":[[%d,"0"],[%d,"0.5"],[%d,"NaN"],[%d,"2"]]}]}}`,end-21600,end,end,end)
 }));defer server.Close()
 samples,err:=queryTrend(context.Background(),&Dependencies{MetricsURL:server.URL,Client:server.Client()},"team","cluster",now)
 require.NoError(t,err);require.Len(t,samples,2);require.Zero(t,samples[0].Value);require.NotContains(t,samples[0].Labels,"secret")
}
