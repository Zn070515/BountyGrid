(async()=>{
  const b=[...document.querySelectorAll('.cs-pagination-item')].find(x=>(x.innerText||'').trim()===String(%d));
  if(!b) return JSON.stringify({ok:false});
  const before=(document.querySelector('.card-item')||{}).innerText||'';
  b.click();
  for(let i=0;i<80;i++){await new Promise(r=>setTimeout(r,100)); const now=(document.querySelector('.card-item')||{}).innerText||''; if(now&&now!==before) break;}
  return JSON.stringify({ok:true});
})()
