const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');

function harness() {
    const handlers = new Map(), timers = new Map(), sources = [];
    let timerId = 0, writes = 0, html = '<div>connected</div>', observer;
    const panel = {isConnected:true, dataset:{stage:'connected',version:'initial',eventsUrl:'/s/whatsqr/connections/16/pair/events',liveLabel:'Live',connectingLabel:'Connecting',pausedLabel:'Paused',refreshError:'Unavailable'}};
    Object.defineProperty(panel,'innerHTML',{get:()=>html,set:v=>{html=v;writes++;}});
    const label = {textContent:''}, indicator={dataset:{},querySelector:()=>label}, error={hidden:true};
    const add=(name,fn)=>handlers.set(name,fn),remove=(name)=>handlers.delete(name);
    const document={hidden:false,readyState:'complete',body:{},getElementById:id=>({'whatsqr-pair-status':panel,'whatsqr-live-status':indicator,'whatsqr-refresh-error':error}[id]),addEventListener:add,removeEventListener:remove};
    class Source {
        constructor(url){this.url=url;this.handlers={};sources.push(this);}
        addEventListener(name,fn){this.handlers[name]=fn;}
        emit(name,data={},version=''){this.handlers[name]?.({data:JSON.stringify(data),lastEventId:version});}
        close(){this.closed=true;}
    }
    const window={EventSource:Source,location:{href:'https://example.test/s/pair'},addEventListener:add,removeEventListener:remove};
    const context={document,window,EventSource:Source,URL,MutationObserver:class{constructor(fn){observer=fn;}observe(){}disconnect(){}},setTimeout:(fn,delay)=>{timers.set(++timerId,{fn,delay});return timerId;},clearTimeout:id=>timers.delete(id)};
    vm.runInNewContext(fs.readFileSync(__dirname+'/../../Assets/js/whatsqr.js','utf8'),context);
    return {panel,document,window,indicator,error,handlers,timers,sources,removed:()=>observer(),writes:()=>writes};
}

test('connected account continues listening and snapshots do not shift the card',()=>{
    const h=harness();assert.equal(h.sources.length,1);
    const s=h.sources[0];assert.match(s.url,/version=initial/);s.emit('live');assert.equal(h.indicator.dataset.state,'live');
    s.emit('pairing',{stage:'connected',html:'<div>connected</div>'},'initial');assert.equal(h.writes(),0);
    s.emit('pairing',{stage:'reconnecting',html:'<div>Reconnecting</div>'},'new');assert.equal(h.panel.dataset.stage,'reconnecting');assert.equal(h.writes(),1);
    h.window.Mautic.whatsqrOnLoad();assert.equal(h.sources.length,1);
});
test('rotation reconnects with cursor without an error or layout update',()=>{
    const h=harness();h.sources[0].emit('live');h.sources[0].emit('rotate');assert.equal(h.sources[0].closed,true);
    const timer=[...h.timers.values()][0];assert.equal(timer.delay,250);timer.fn();assert.equal(h.sources.length,2);assert.equal(h.error.hidden,true);assert.equal(h.writes(),0);
});
test('hidden and removed pages close streams and restore state on return',()=>{
    const h=harness();h.document.hidden=true;h.handlers.get('visibilitychange')();assert.equal(h.sources[0].closed,true);assert.equal(h.indicator.dataset.state,'paused');
    h.document.hidden=false;h.handlers.get('visibilitychange')();assert.equal(h.sources.length,2);
    h.panel.isConnected=false;h.removed();assert.equal(h.sources[1].closed,true);assert.equal(h.handlers.size,0);assert.equal(h.timers.size,0);
});
test('failure preserves card and backs off; stale sources cannot overwrite UI',()=>{
    const h=harness(),s=h.sources[0];s.emit('unavailable');assert.equal(h.writes(),0);assert.equal(h.error.hidden,false);assert.equal([...h.timers.values()][0].delay,5000);
    [...h.timers.values()][0].fn();s.emit('pairing',{stage:'not_done',html:'wrong'});assert.equal(h.writes(),0);
    h.sources[1].onerror();assert.equal([...h.timers.values()][0].delay,10000);
    h.window.Mautic.whatsqrOnUnload();assert.equal(h.timers.size,0);
});
