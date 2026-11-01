(() => {
  const cards=[...document.querySelectorAll('.plan-exclusive-src .company-card')];
  const out=cards.slice(%d,%d).map(c=>{
    const name=((c.querySelector('.name')||{}).textContent||'').trim();
    const text=(c.textContent||'').replace(/\s+/g,' ').trim();
    return {name,text:text.slice(0,220),buttons:[...c.querySelectorAll('button')].map(b=>(b.textContent||'').trim())};
  });
  return JSON.stringify({cards:out,total:cards.length,noData:(document.body.textContent||'').includes('暂无数据')});
})()
