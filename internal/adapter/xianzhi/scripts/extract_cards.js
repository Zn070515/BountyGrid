(() => {
  const cards=[...document.querySelectorAll('.card-item')];
  const out=cards.slice(%d,%d).map(c=>{
    const parts=(c.innerText||'').replace(/\n+/g,'|').split('|');
    const val=k=>{const i=parts.indexOf(k+'：'); return (i>=0 && i+1<parts.length)?parts[i+1].trim():''};
    const k=Object.keys(c).find(x=>x.startsWith('__reactFiber'));
    let f=k?c[k]:null,p=null;
    for(let i=0;i<8&&f;i++){const mp=f.memoizedProps||{};if(mp.task){p=mp.task;break;}f=f.return;}
    if(!p) return {missing:true,title:parts[0]||''};
    return {id:p.id,name:p.name||'',status:p.status,type:p.type,registrationBegin:p.registrationBegin||0,registrationEnd:p.registrationEnd||0,taskBegin:p.taskBegin||0,taskEnd:p.taskEnd||0,registrationLimit:p.registrationLimit||0,bounty:val('赏金范围'),buttons:[...c.querySelectorAll('button')].map(b=>(b.innerText||'').trim()),title:parts[0]||''};
  });
  const pages=[...document.querySelectorAll('.cs-pagination-item')].map(x=>parseInt((x.innerText||'').trim(),10)).filter(Number.isFinite);
  return JSON.stringify({cards:out,maxPage:pages.length?Math.max(...pages):0});
})()
