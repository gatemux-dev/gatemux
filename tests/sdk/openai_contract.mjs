import OpenAI from 'openai';
import assert from 'node:assert/strict';

const base = process.env.GATEMUX_SDK_FIXTURE_URL;
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(base).hostname), 'local fixture only');
const model = process.env.GATEMUX_SDK_FIXTURE_ALIAS;
const client = new OpenAI({ apiKey: process.env.GATEMUX_SDK_FIXTURE_KEY, baseURL: `${base}/v1`, maxRetries: 0, timeout: 10000 });
const models = await client.models.list();
assert(models.data.some(item => item.id === model));
const chat = await client.chat.completions.create({model, messages: [{role:'user',content:'hello'}]});
assert.equal(chat.model, model); assert.equal(chat.choices[0].message.content, 'ok');
let chunks = 0;
for await (const chunk of await client.chat.completions.create({model,messages:[{role:'user',content:'hello'}],stream:true})) { assert.equal(chunk.model,model); chunks++; }
assert(chunks > 0);
const embeddings = await client.embeddings.create({model,input:[[1,2],[3]],encoding_format:'float'});
assert.equal(embeddings.model,model);assert.deepEqual(embeddings.data[0].embedding,[0.1,0.2,0.3]);
const response = await client.responses.create({model,input:'hello',tools:[{type:'function',name:'lookup',parameters:{type:'object',properties:{}}}]});
assert(response.id.startsWith('resp_gatemux_'));assert.equal(response.output_text,'hello');
assert.equal((await client.responses.retrieve(response.id)).id,response.id);
const followup = await client.responses.create({model,input:'next',previous_response_id:response.id});
assert.equal(followup.previous_response_id,response.id);
const events = [];
for await (const event of await client.responses.create({model,input:'stream',stream:true})) events.push(event);
assert.equal(events[0].type,'response.created');assert.equal(events.at(-1).type,'response.completed');
assert.equal(events.at(-1).response.model,model);
await client.responses.inputItems.list(response.id);
await client.responses.delete(response.id);
await assert.rejects(client.responses.retrieve(response.id),error => error.status === 404);
console.log('PASS: official Node SDK models, chat/stream, token embeddings and Responses lifecycle');
