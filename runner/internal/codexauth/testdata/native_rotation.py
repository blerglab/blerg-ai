# Synthetic credentials and loopback endpoints only. No workstation login is read.
import base64, json, os, pathlib, select, shutil, subprocess, threading, time
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
root=pathlib.Path(os.environ["CODEX_ROTATION_FIXTURE_ROOT"])
claims={'exp':int(time.time()+3600),'sub':'fixture-user','https://api.openai.com/auth':{'chatgpt_account_id':'fixture-account','chatgpt_plan_type':'pro'}}
def jwt(exp):
    c=dict(claims,exp=exp)
    return 'eyJhbGciOiJub25lIn0.'+base64.urlsafe_b64encode(json.dumps(c).encode()).decode().rstrip('=')+'.fixture'
stats={'refreshes':0,'reused':0}
class Handler(BaseHTTPRequestHandler):
    def log_message(self,*a): pass
    def do_POST(self):
        raw=self.rfile.read(int(self.headers.get('Content-Length',0)))
        try: req=json.loads(raw)
        except Exception: req={}
        stats['refreshes']+=1
        if req.get('refresh_token')=='fixture-initial' and stats['refreshes']==1:
            self.send_response(200); self.end_headers()
            self.wfile.write(json.dumps({'access_token':jwt(int(time.time()+3600)), 'refresh_token':'fixture-next','id_token':jwt(int(time.time()+3600))}).encode())
        else:
            stats['reused']+=1
            self.send_response(401); self.end_headers()
            self.wfile.write(b'{"error":{"code":"refresh_token_reused","message":"Your access token could not be refreshed because your refresh token was already used."}}')
server=ThreadingHTTPServer(('127.0.0.1',0),Handler)
thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
def consumer(label):
    home=root/('native-'+label);home.mkdir(exist_ok=True)
    auth=home/'auth.json';auth.write_text(json.dumps({'auth_mode':'chatgpt','tokens':{'access_token':jwt(int(time.time()-3600)),'refresh_token':'fixture-initial','id_token':jwt(int(time.time()+3600)),'account_id':'fixture-account'},'last_refresh':'2020-01-01T00:00:00Z'}));auth.chmod(0o600)
    env={'HOME':str(home),'CODEX_HOME':str(home),'PATH':'/usr/local/bin:/usr/bin:/bin','CODEX_REFRESH_TOKEN_URL_OVERRIDE':f'http://127.0.0.1:{server.server_port}/oauth/token','HTTPS_PROXY':'http://127.0.0.1:9','HTTP_PROXY':'http://127.0.0.1:9','ALL_PROXY':'http://127.0.0.1:9','NO_PROXY':'127.0.0.1,localhost'}
    p=subprocess.Popen([shutil.which('codex'),'app-server'],env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
    def call(i,method,params):
        p.stdin.write((json.dumps({'id':i,'method':method,'params':params})+'\n').encode());p.stdin.flush()
        deadline=time.monotonic()+15
        while time.monotonic()<deadline:
            if select.select([p.stdout],[],[],max(0,deadline-time.monotonic()))[0]:
                msg=json.loads(p.stdout.readline())
                if msg.get('id')==i:return msg
        raise RuntimeError('native RPC deadline')
    try:
        call(1,'initialize',{'clientInfo':{'name':'fixture','version':'1'},'capabilities':{'experimentalApi':True}})
        p.stdin.write(b'{"method":"initialized"}\n');p.stdin.flush()
        msg=call(2,'account/read',{'refreshToken':True})
        stored=json.loads(auth.read_text())
        return {'account_returned':bool(msg.get('result',{}).get('account')),'rotated_file':stored.get('tokens',{}).get('refresh_token')=='fixture-next','rpc_error':bool(msg.get('error'))}
    finally:
        p.terminate()
        try:p.wait(timeout=3)
        except subprocess.TimeoutExpired:p.kill();p.wait()
try:
    first=consumer('desktop');second=consumer('copy')
    print(json.dumps({'version':subprocess.check_output([shutil.which('codex'),'--version'],text=True).strip(),'desktop':first,'copied_pod':second,'provider':stats}))
finally:server.shutdown();server.server_close();thread.join()
