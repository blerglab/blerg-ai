package daemon

import "fmt"

type workspaceProbeUncertain struct{ err error }

func (e *workspaceProbeUncertain) Error() string {
	return fmt.Sprintf("runtime probe acknowledgement uncertain: %v", e.err)
}

// The head probe deliberately does not start Git. Even rev-parse reads includes
// from agent-written config. This fixed single-process reader never forks or
// invokes external commands, bounds every read and rejects special files.
const workspaceHeadProbe = `import os,sys,stat,signal,resource,re
resource.setrlimit(resource.RLIMIT_AS,(64*1024*1024,64*1024*1024))
signal.signal(signal.SIGALRM,lambda *a:sys.exit(124))
signal.setitimer(signal.ITIMER_REAL,float(sys.argv[1]))
root=os.open('.git',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
def read(name,limit):
 parts=name.split('/')
 if any(p in ('','..','.') for p in parts): raise ValueError('invalid ref')
 d=os.dup(root)
 try:
  for p in parts[:-1]:
   n=os.open(p,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=d)
   os.close(d);d=n
  f=os.open(parts[-1],os.O_RDONLY|os.O_NONBLOCK|os.O_NOFOLLOW,dir_fd=d)
  try:
   s=os.fstat(f)
   if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or s.st_size>limit: raise ValueError('invalid ref file')
   b=os.read(f,limit+1)
   if len(b)>limit: raise ValueError('ref too large')
   return b.decode('ascii').strip()
  finally: os.close(f)
 finally: os.close(d)
def commit(v):
 if re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}',v): return v
 raise ValueError('invalid commit')
h=read('HEAD',512)
if not h.startswith('ref: '): print(commit(h));sys.exit(0)
r=h[5:]
if not re.fullmatch(r'refs/heads/[A-Za-z0-9._/-]{1,200}',r) or '..' in r or '//' in r or r.endswith('/'): raise ValueError('invalid ref')
try: print(commit(read(r,512)));sys.exit(0)
except FileNotFoundError: pass
try:
 for line in read('packed-refs',4*1024*1024).splitlines():
  v,sep,n=line.partition(' ')
  if sep and n==r: print(commit(v));sys.exit(0)
except FileNotFoundError: pass
print('')
`

const workspaceProbeCleanup = `import os,sys,signal,hashlib,time
signal.signal(signal.SIGALRM,lambda *a:sys.exit(124))
signal.alarm(2)
expected=sys.argv[1]
victims=[]
for entry in os.listdir('/proc'):
 if not entry.isdigit(): continue
 try:
  with open('/proc/'+entry+'/cmdline','rb') as f: args=f.read(32768).split(b'\0')
  if b'-c' not in args: continue
  i=args.index(b'-c')+1
  if i>=len(args) or hashlib.sha256(args[i]).hexdigest()!=expected: continue
  os.kill(int(entry),signal.SIGKILL)
  victims.append(entry)
 except (FileNotFoundError,ProcessLookupError): pass
# SIGKILL submission is not a drain acknowledgement. Wait for exit (or a
# reaped/no-memory zombie); a blocked kernel worker keeps the runtime fenced.
while victims:
 pending=[]
 for entry in victims:
  try:
   with open('/proc/'+entry+'/stat','r') as f: state=f.read(4096).split(') ',1)[1].split()[0]
   if state!='Z': pending.append(entry)
  except FileNotFoundError: pass
 victims=pending
 if victims: time.sleep(.005)
print('clean')
`
